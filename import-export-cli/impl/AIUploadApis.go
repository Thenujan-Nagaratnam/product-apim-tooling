/*
*  Copyright (c) WSO2 Inc. (http://www.wso2.org) All Rights Reserved.
*
*  WSO2 Inc. licenses this file to you under the Apache License,
*  Version 2.0 (the "License"); you may not use this file except
*  in compliance with the License.
*  You may obtain a copy of the License at
*
*    http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing,
* software distributed under the License is distributed on an
* "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
* KIND, either express or implied.  See the License for the
* specific language governing permissions and limitations
* under the License.
 */

package impl

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-resty/resty/v2"
	"github.com/wso2/product-apim-tooling/import-export-cli/credentials"
	"github.com/wso2/product-apim-tooling/import-export-cli/utils"
)

var OnPremKey string
var uploadedAPIs int32
var totalAPIs int32
var Endpoint = utils.DefaultAIEndpoint
var PublisherEndpoint string
var Credential credentials.Credential
var ExportAPIPreserveStatus = false
var RunningExportApiCommand = false
var CmdUsername = ""
var ExportAllRevisions = false
var CmdUploadEnvironment = ""

func RemoveExistingAPIs() error {
	headers := make(map[string]string)
	headers["API-KEY"] = OnPremKey

	var resp *resty.Response
	var deleteErr error

	for attempt := 1; attempt <= 2; attempt++ {
		resp, deleteErr = utils.InvokeDELETERequest(Endpoint+"/ai/spec-populator/bulk-remove", headers)
		if deleteErr != nil {
			fmt.Printf("Error removing existing APIs (attempt %d): %v\n", attempt, deleteErr)
			continue
		}

		if resp.StatusCode() != 200 {
			fmt.Printf("Removing existing APIs failed with status %d %s (attempt %d)\n", resp.StatusCode(), resp.Body(), attempt)
			continue
		}

		jsonResp := map[string]map[string]int32{}

		err := json.Unmarshal(resp.Body(), &jsonResp)

		if err != nil {
			utils.HandleErrorAndContinue("Error in unmarshalling response:", err)
			continue
		}

		fmt.Printf("Removed %d APIs successfully from vector database (attempt %d)\n", jsonResp["message"]["delete_count"], attempt)
		return nil
	}

	if deleteErr != nil {
		return fmt.Errorf("Error removing existing APIs after retry: %v", deleteErr)
	}
	return fmt.Errorf("Removing existing APIs failed after retry")
}

func UploadAPIs(credential credentials.Credential, cmdUploadEnvironment, authToken, endpointUrl, CmdUsername string, exportAPIPreserveStatus, runningExportApiCommand, exportAPIsAllRevisions bool) {

	OnPremKey = authToken
	Endpoint = endpointUrl
	CmdUploadEnvironment = cmdUploadEnvironment
	Credential = credential
	ExportAPIPreserveStatus = exportAPIPreserveStatus
	RunningExportApiCommand = runningExportApiCommand
	ExportAllRevisions = exportAPIsAllRevisions
	PublisherEndpoint = utils.GetPublisherEndpointOfEnv(cmdUploadEnvironment, utils.MainConfigFilePath)

	fmt.Println("Uploading public APIs to vector DB...")

	accessToken, preCommandErr := credentials.GetOAuthAccessToken(credential, cmdUploadEnvironment)

	if preCommandErr != nil {
		utils.HandleErrorAndExit("Error getting access token", preCommandErr)
	}

	apiListQueue := make(chan []map[string]interface{}, 10)

	go ProduceAPIPayloads(accessToken, apiListQueue)

	numConsumers := 3
	var wg sync.WaitGroup
	for i := 0; i < numConsumers; i++ {
		wg.Add(1)
		go ConsumeAPIPayloads(apiListQueue, &wg)
	}

	wg.Wait()

	fmt.Printf("\nTotal number of public APIs present in the API Manager: %d\nTotal number of APIs successfully uploaded: %d\n\n", totalAPIs, uploadedAPIs)
}

func ProduceAPIPayloads(accessToken string, apiListQueue chan<- []map[string]interface{}) {
	ProcessTenants(accessToken, "tenants?state=active&limit=100&offset=0", apiListQueue)
	close(apiListQueue)
}

// process all the tenants
func ProcessTenants(accessToken, endpointPath string, apiListQueue chan<- []map[string]interface{}) {
	PublisherEndpoint = utils.AppendSlashToString(PublisherEndpoint)

	requestURL := PublisherEndpoint + endpointPath
	utils.Logln(utils.LogPrefixInfo+"ExportAPI: URL:", requestURL)
	headers := make(map[string]string)
	headers[utils.HeaderAuthorization] = utils.HeaderValueAuthBearerPrefix + " " + accessToken
	headers[utils.HeaderAccept] = utils.JsonArrayFormatType

	resp, err := utils.InvokeGETRequest(requestURL, headers)

	if err != nil {
		fmt.Println("Error in getting tenants:", err)
		return
	}

	tenantListResponse := &utils.TenantListResponse{}
	fmt.Println(tenantListResponse.List)
	err = json.Unmarshal([]byte(resp.Body()), tenantListResponse)
	if err != nil {
		fmt.Println("Error unmarshalling tenant list response:", err)
		return
	}

	tenantCount := tenantListResponse.Pagination.Total
	if tenantCount == 0 {
		// Handle carbon.super tenant
		fmt.Println("Processing tenant:", utils.DefaultTenantDomain)
		ProcessAPIs(accessToken, utils.DefaultTenantDomain, "apis?limit=10&offset=0", apiListQueue)
	} else {
		// Handle all tenants
		for _, tenant := range tenantListResponse.List {
			fmt.Println("Processing tenant:", tenant.Domain)
			ProcessAPIs(accessToken, tenant.Domain, "apis?limit=10&offset=0", apiListQueue)
		}
	}

	// Process next set of tenants
	if tenantListResponse.Pagination.Next != "" {
		ProcessTenants(accessToken, tenantListResponse.Pagination.Next, apiListQueue)
	}
}

// process apis in a tenant
func ProcessAPIs(accessToken, tenant, endpointPath string, apiListQueue chan<- []map[string]interface{}) {
	apiListOffset = 0
	startingApiIndexFromList = 0
	count, apis = getAPIList(Credential, CmdUploadEnvironment, tenant)
	ExportAPIsAI(tenant, apiListQueue)
}

// get apiList from the queue and upload them
func ConsumeAPIPayloads(apiListQueue <-chan []map[string]interface{}, wg *sync.WaitGroup) {
	defer wg.Done()

	for apiList := range apiListQueue {
		InvokePOSTRequest(apiList)
	}
}

// InvokePOSTRequest uploads the APIs to the vector DB
func InvokePOSTRequest(apiList []map[string]interface{}) {
	fmt.Println("apiList", len(apiList))
	fmt.Printf("Uploading %d APIs for tenant: %s\n", len(apiList), apiList[0]["tenant_domain"])
	payload, err := json.Marshal(map[string]interface{}{"apis": apiList})
	if err != nil {
		utils.HandleErrorAndContinue("Error in marshalling payload:", err)
	}

	headers := make(map[string]string)
	headers["API-KEY"] = OnPremKey
	headers[utils.HeaderContentType] = utils.HeaderValueApplicationJSON

	var resp *resty.Response
	var uploadErr error

	for attempt := 1; attempt <= 2; attempt++ {
		resp, uploadErr = utils.InvokePOSTRequest(Endpoint+"/ai/spec-populator/bulk-upload", headers, payload)
		if uploadErr != nil {
			fmt.Printf("API upload failed (attempt %d). Reason: %v\n", attempt, uploadErr)
			continue
		}

		if resp.StatusCode() != 200 {
			fmt.Printf("Failed to upload %d APIs for tenant %s with status %d %s (attempt %d).\n", len(apiList), apiList[0]["tenant_domain"], resp.StatusCode(), resp.Body(), attempt)
			continue
		}

		jsonResp := map[string]map[string]int32{}

		err := json.Unmarshal(resp.Body(), &jsonResp)

		if err != nil {
			utils.HandleErrorAndContinue("Error in unmarshalling response:", err)
			continue
		}

		fmt.Printf("%d APIs uploaded successfully for tenant: %s (attempt %d)\n", len(apiList), apiList[0]["tenant_domain"], attempt)
		atomic.AddInt32(&uploadedAPIs, jsonResp["message"]["upsert_count"])
		break
	}

	if uploadErr != nil {
		utils.HandleErrorAndContinue("API upload failed after retry. Reason: ", uploadErr)
	}
}

// Do the API exportation
func ExportAPIsAI(cmdResourceTenantDomain string, apiListQueue chan<- []map[string]interface{}) {
	if count == 0 {
		fmt.Println("No APIs available to be exported..!")
	} else {

		var counterSuceededAPIs = 0
		for count > 0 {
			atomic.AddInt32(&totalAPIs, count)
			accessToken, preCommandErr := credentials.GetOAuthAccessToken(Credential, CmdUploadEnvironment)
			if preCommandErr == nil {
				apiList := []map[string]interface{}{}
				for i := startingApiIndexFromList; i < len(apis); i++ {
					apiPayload := exportAPIandReturn(apis[i], accessToken, CmdUploadEnvironment, ExportAPIPreserveStatus)
					if apiPayload != nil {
						apiList = append(apiList, apiPayload)
					}
					counterSuceededAPIs++
				}
				apiListQueue <- apiList
			} else {
				fmt.Println("Error getting OAuth Tokens : " + preCommandErr.Error())
			}
			apiListOffset += utils.MaxAPIsToExportOnce
			count, apis = getAPIList(Credential, CmdUploadEnvironment, cmdResourceTenantDomain)
			startingApiIndexFromList = 0
		}
	}
}

// Export the API and archive to zip format
func exportAPIandReturn(api utils.API, accessToken, cmdExportEnvironment string, exportAPIPreserveStatus bool) map[string]interface{} {

	resp, err := ExportAPIFromEnv(accessToken, api.Name, api.Version, "",
		api.Provider, "json", cmdExportEnvironment, exportAPIPreserveStatus, true)
	if err != nil {
		utils.HandleErrorAndContinue("Error getting zip file", err)
	}

	if resp.StatusCode() == http.StatusOK {

		zipReader, err := zip.NewReader(bytes.NewReader(resp.Body()), int64(len(resp.Body())))
		if err != nil {
			utils.HandleErrorAndContinue("Error reading zip file", err)
		}

		apiPayload := map[string]interface{}{}

		for _, file := range zipReader.File {
			apiPayload = ReadZipFile(file, apiPayload)
		}
		return apiPayload
	} else {
		fmt.Println("Error exporting API:", api.Name, "-", api.Version, " of Provider:", api.Provider)
		utils.PrintErrorResponseAndExit(resp)
		return nil
	}
}

func ReadZipFile(file *zip.File, apiPayload map[string]interface{}) map[string]interface{} {
	fileReader, err := file.Open()
	if err != nil {
		utils.HandleErrorAndContinue("Error while opening file", err)
	}
	defer fileReader.Close()

	fileContents, err := ioutil.ReadAll(fileReader)
	if err != nil {
		utils.HandleErrorAndContinue("Error while reading file", err)
	}

	if strings.HasSuffix(file.Name, "api.json") {
		var jsonResp map[string]interface{}
		if err := json.Unmarshal(fileContents, &jsonResp); err != nil {
			utils.HandleErrorAndContinue("Error unmarshalling YAML content: %v\n", err)
		}

		data, _ := jsonResp["data"].(map[string]interface{})

		if data["visibility"] != "PUBLIC" {
			return nil
		}

		apiPayload["uuid"] = data["id"].(string)
		apiPayload["api_name"] = data["name"].(string)
		apiPayload["version"] = data["version"].(string)
		apiPayload["tenant_domain"] = data["organizationId"].(string)
		apiPayload["api_type"] = data["type"].(string)

	} else if strings.HasSuffix(file.Name, "swagger.json") {
		var jsonResp map[string]interface{}
		if err := json.Unmarshal(fileContents, &jsonResp); err != nil {
			utils.HandleErrorAndContinue("Error unmarshalling YAML content: %v\n", err)
		}
		info, _ := jsonResp["info"].(map[string]interface{})
		description, _ := info["description"].(string)
		apiPayload["description"] = description
		apiPayload["api_spec"] = string(fileContents)
	} else if strings.HasSuffix(file.Name, "schema.json") {
		var jsonResp map[string]interface{}
		if err := json.Unmarshal(fileContents, &jsonResp); err != nil {
			utils.HandleErrorAndContinue("Error unmarshalling YAML content: %v\n", err)
		}
		info, _ := jsonResp["info"].(map[string]interface{})
		description, _ := info["description"].(string)
		apiPayload["description"] = description
		apiPayload["sdl_schema"] = fileContents
	} else if strings.HasSuffix(file.Name, "asyncapi.json") {
		var jsonResp map[string]interface{}
		if err := json.Unmarshal(fileContents, &jsonResp); err != nil {
			utils.HandleErrorAndContinue("Error unmarshalling YAML content: %v\n", err)
		}
		info, _ := jsonResp["info"].(map[string]interface{})
		description, _ := info["description"].(string)
		apiPayload["description"] = description
		apiPayload["async_spec"] = fileContents
	}

	return apiPayload
}