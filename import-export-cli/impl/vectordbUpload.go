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
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/go-resty/resty/v2"
	"github.com/wso2/product-apim-tooling/import-export-cli/credentials"
	"github.com/wso2/product-apim-tooling/import-export-cli/utils"
)

var onPremKey string
var uploadedAPIs int32
var totalAPIs int32
var endpoint string

func RemoveExistingAPIs() error {
	headers := make(map[string]string)
	headers["API-KEY"] = onPremKey

	var resp *resty.Response
	var deleteErr error

	for attempt := 1; attempt <= 2; attempt++ {
		resp, deleteErr = utils.InvokeDELETERequest(endpoint+"/ai/spec-populator/bulk-remove", headers)
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

func UploadAPIs(credential credentials.Credential, cmdUploadEnvironment, authToken, endpointUrl string) {

	onPremKey = authToken
	endpoint = endpointUrl

	devPortalEndpoint := utils.GetDevPortalEndpointOfEnv(cmdUploadEnvironment, utils.MainConfigFilePath)

	fmt.Println("Removing existing APIs from vector DB..!")
	err := RemoveExistingAPIs()
	if err != nil {
		utils.HandleErrorAndExit("Error in removing existing APIs", err)
	}

	fmt.Println("Uploading public APIs to vector DB...")

	// buffered channel with 10 slots
	apiListQueue := make(chan []map[string]interface{}, 10)

	// producer
	go ProduceAPIPayloads(devPortalEndpoint, apiListQueue)

	// consumer
	numConsumers := utils.MarketplaceAssistantThreadSize
	var wg sync.WaitGroup
	for i := 0; i < numConsumers; i++ {
		wg.Add(1)
		go ConsumeAPIPayloads(apiListQueue, &wg)
	}

	wg.Wait()

	fmt.Printf("\nTotal number of public APIs present in the API Manager: %d\nTotal number of APIs successfully uploaded: %d\n\n", totalAPIs, uploadedAPIs)
}

func InvokeGETRequest(requestURL, tenant string) (*resty.Response, error) {
	utils.Logln(utils.LogPrefixInfo+"ExportAPI: URL:", requestURL)
	headers := make(map[string]string)
	headers["x-wso2-tenant"] = tenant
	headers[utils.HeaderAccept] = utils.JsonArrayFormatType

	return utils.InvokeGETRequest(requestURL, headers)
}

func ProduceAPIPayloads(devPortalEndpoint string, apiListQueue chan<- []map[string]interface{}) {
	ProcessTenants(devPortalEndpoint, "tenants?state=active&limit=100&offset=0", apiListQueue)
	close(apiListQueue)
}
// process all the tenants
func ProcessTenants(devPortalEndpoint, endpointPath string, apiListQueue chan<- []map[string]interface{}) {
	devPortalEndpoint = utils.AppendSlashToString(devPortalEndpoint)

	requestURL := devPortalEndpoint + endpointPath
	resp, err := InvokeGETRequest(requestURL, "")
	if err != nil {
		fmt.Println("Error in getting tenants:", err)
		return
	}

	tenantListResponse := &utils.TenantListResponse{}
	err = json.Unmarshal([]byte(resp.Body()), tenantListResponse)
	if err != nil {
		fmt.Println("Error unmarshalling tenant list response:", err)
		return
	}

	tenantCount := tenantListResponse.Pagination.Total
	if tenantCount == 0 {
		// Handle carbon.super tenant
		fmt.Println("Processing tenant:", utils.DefaultTenantDomain)
		ProcessAPIs(devPortalEndpoint, utils.DefaultTenantDomain, "apis?limit=10&offset=0", apiListQueue)
	} else {
		// Handle all tenants
		for _, tenant := range tenantListResponse.List {
			fmt.Println("Processing tenant:", tenant.Domain)
			ProcessAPIs(devPortalEndpoint, tenant.Domain, "apis?limit=10&offset=0", apiListQueue)
		}
	}

	// Process next set of tenants
	if tenantListResponse.Pagination.Next != "" {
		ProcessTenants(devPortalEndpoint, tenantListResponse.Pagination.Next, apiListQueue)
	}
}

// process apis in a tenant
func ProcessAPIs(devPortalEndpoint, tenant, endpointPath string, apiListQueue chan<- []map[string]interface{}) {
	requestURL := devPortalEndpoint + endpointPath

	resp, err := InvokeGETRequest(requestURL, tenant)
	if err != nil {
		utils.HandleErrorAndContinue("Error in getting APIs for tenant: "+tenant, err)
	}

	apiListResponse := &utils.UploadAPIListResponse{}
	err = json.Unmarshal([]byte(resp.Body()), apiListResponse)
	if err != nil {
		utils.HandleErrorAndContinue("Error unmarshalling API list response:", err)
	}

	// Update totalAPIs count
	atomic.AddInt32(&totalAPIs, apiListResponse.Count)

	apiList := []map[string]interface{}{}

	for _, api := range apiListResponse.List {
		apiPayload := map[string]interface{}{
			"uuid":          api.ID,
			"description":   api.Description,
			"api_name":      api.Name,
			"version":       api.Version,
			"tenant_domain": tenant,
			"api_type":      api.Type,
		}

		switch api.Type {
		case "HTTP", "APIPRODUCT", "REST", "SOAP", "SOAPTOREST":
			requestURL := devPortalEndpoint + "apis/" + api.ID + "/swagger"
			swaggerResp, err := InvokeGETRequest(requestURL, tenant)
			if err == nil {
				apiPayload["api_spec"] = swaggerResp.String()
			} else {
				utils.HandleErrorAndContinue("Error in getting swagger for API: "+api.ID, err)
			}

		case "GRAPHQL":
			requestURL := devPortalEndpoint + "apis/" + api.ID + "/graphql-schema"
			schemaResp, err := InvokeGETRequest(requestURL, tenant)
			if err == nil {
				apiPayload["sdl_schema"] = schemaResp.String()
			} else {
				utils.HandleErrorAndContinue("Error in getting Graphql schema for API: "+api.ID, err)
			}

		case "WS", "WEBSUB", "ASYNC", "SSE", "WEBHOOK":
			requestURL := devPortalEndpoint + "apis/" + api.ID + "/async-api-specification"
			asyncResp, err := InvokeGETRequest(requestURL, tenant)
			if err == nil {
				apiPayload["async_spec"] = asyncResp.String()
			} else {
				utils.HandleErrorAndContinue("Error in getting async spec for API: "+api.ID, err)
			}
		}
		apiList = append(apiList, apiPayload)

	}
	apiListQueue <- apiList

	// Process next set of APIs
	if apiListResponse.Pagination.Next != "" {
		ProcessAPIs(devPortalEndpoint, tenant, apiListResponse.Pagination.Next, apiListQueue)
	}
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
	fmt.Printf("Uploading %d APIs for tenant: %s\n", len(apiList), apiList[0]["tenant_domain"])
	payload, err := json.Marshal(map[string]interface{}{"apis": apiList})
	if err != nil {
		utils.HandleErrorAndContinue("Error in marshalling payload:", err)
	}

	headers := make(map[string]string)
	headers["API-KEY"] = onPremKey
	headers[utils.HeaderContentType] = utils.HeaderValueApplicationJSON

	var resp *resty.Response
	var uploadErr error

	for attempt := 1; attempt <= 2; attempt++ {
		resp, uploadErr = utils.InvokePOSTRequest(endpoint+"/ai/spec-populator/bulk-upload", headers, payload)
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
