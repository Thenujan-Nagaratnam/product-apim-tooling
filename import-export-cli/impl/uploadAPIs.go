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

func removeExistingAPIs() error {
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
			fmt.Printf("Removing existing APIs failed with status %d (attempt %d)\n", resp.StatusCode(), attempt)
			continue
		}

		fmt.Printf("Existing APIs removed successfully (attempt %d)\n", attempt)
		return nil
	}

	if deleteErr != nil {
		return fmt.Errorf("Error removing existing APIs after retry: %v", deleteErr)
	}
	return fmt.Errorf("Removing existing APIs failed after retry")
}

func UploadAPIs(credential credentials.Credential, cmdUploadEnvironment string, cmdResourceTenantDomain string, cmdUsername, authToken, endpointUrl string) {

	onPremKey = authToken
	endpoint = endpointUrl

	devPortalEndpoint := utils.GetDevPortalEndpointOfEnv(cmdUploadEnvironment, utils.MainConfigFilePath)

	fmt.Println("Removing existing APIs from vector DB..!")
	err := removeExistingAPIs()
	if err != nil {
		utils.HandleErrorAndExit("Error in removing existing APIs", err)
	}

	fmt.Println("Uploading APIs to vector DB...")

	payloadQueue := make(chan []map[string]string, 2)

	go produceAPIPayloads(devPortalEndpoint, payloadQueue)

	numConsumers := 2
	var wg sync.WaitGroup
	for i := 0; i < numConsumers; i++ {
		wg.Add(1)
		go consumeAPIPayloads(payloadQueue, &wg)
	}

	wg.Wait()

	fmt.Printf("\n%d APIs uploaded out of %d APIs.\n", totalAPIs, uploadedAPIs)
}

func InvokeGETRequest(requestURL, tenant string) (*resty.Response, error) {
	utils.Logln(utils.LogPrefixInfo+"ExportAPI: URL:", requestURL)
	headers := make(map[string]string)
	headers["x-wso2-tenant"] = tenant
	headers[utils.HeaderAccept] = utils.JsonArrayFormatType

	return utils.InvokeGETRequest(requestURL, headers)
}

func produceAPIPayloads(devPortalEndpoint string, payloadQueue chan<- []map[string]string) {
	processTenants(devPortalEndpoint, "tenants?state=active&limit=100&offset=0", payloadQueue)
	close(payloadQueue)
}

func processTenants(devPortalEndpoint, next string, payloadQueue chan<- []map[string]string) {
	devPortalEndpoint = utils.AppendSlashToString(devPortalEndpoint)

	requestURL := devPortalEndpoint + next
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
		processAPIs(devPortalEndpoint, utils.DefaultTenantDomain, "apis?limit=50&offset=0", payloadQueue)
	} else {
		// Handle all tenants
		for _, tenant := range tenantListResponse.List {
			fmt.Println("Processing tenant:", tenant.Domain)
			processAPIs(devPortalEndpoint, tenant.Domain, "apis?limit=50&offset=0", payloadQueue)
		}
	}

	if tenantListResponse.Pagination.Next != "" {
		processTenants(devPortalEndpoint, tenantListResponse.Pagination.Next, payloadQueue)
	}
}

func processAPIs(devPortalEndpoint, tenant, next string, payloadQueue chan<- []map[string]string) {
	requestURL := devPortalEndpoint + next

	resp, err := InvokeGETRequest(requestURL, tenant)
	if err != nil {
		utils.HandleErrorAndContinue("Error in getting APIs for tenant: "+tenant, err)
	}

	apiListResponse := &utils.UploadAPIListResponse{}
	err = json.Unmarshal([]byte(resp.Body()), apiListResponse)
	if err != nil {
		utils.HandleErrorAndContinue("Error unmarshalling API list response:", err)
	}

	atomic.AddInt32(&totalAPIs, apiListResponse.Pagination.Total)

	payload := []map[string]string{}

	for _, api := range apiListResponse.List {
		apiPayload := map[string]string{
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
		payload = append(payload, apiPayload)

	}
	payloadQueue <- payload
}

func consumeAPIPayloads(payloadQueue <-chan []map[string]string, wg *sync.WaitGroup) {
	defer wg.Done()

	for payload := range payloadQueue {
		InvokePOSTRequest(payload)
	}
}

func InvokePOSTRequest(payload []map[string]string) {
	fmt.Printf("Sending post request for %d APIs for tenant: %s\n", len(payload), payload[0]["tenant_domain"])
	jsonData, err := json.Marshal(map[string]interface{}{"apis": payload})
	if err != nil {
		utils.HandleErrorAndContinue("Error in marshalling payload:", err)
	}

	headers := make(map[string]string)
	headers["API-KEY"] = onPremKey
	headers[utils.HeaderContentType] = utils.HeaderValueApplicationJSON

	var resp *resty.Response
	var uploadErr error

	for attempt := 1; attempt <= 2; attempt++ {
		resp, uploadErr = utils.InvokePOSTRequest(endpoint+"/ai/spec-populator/bulk-upload", headers, jsonData)
		if uploadErr != nil {
			fmt.Printf("API upload failed (attempt %d). Reason: %v\n", attempt, uploadErr)
			continue
		}

		if resp.StatusCode() != 200 {
			fmt.Printf("API upload failed with status %d (attempt %d).\n", resp.StatusCode(), attempt)
			continue
		}

		fmt.Printf("%d APIs uploaded successfully for tenant: %s (attempt %d)\n", len(payload), payload[0]["tenant_domain"], attempt)
		atomic.AddInt32(&uploadedAPIs, int32(len(payload)))
		break
	}

	if uploadErr != nil {
		utils.HandleErrorAndContinue("API upload failed after retry. Reason: ", uploadErr)
	}
}
