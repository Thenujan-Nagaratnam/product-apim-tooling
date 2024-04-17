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
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/wso2/product-apim-tooling/import-export-cli/credentials"
	"github.com/wso2/product-apim-tooling/import-export-cli/utils"
)

var tenants []utils.Tenant
var accessToken string
var onPremKey string

func UploadAPIs(credential credentials.Credential, cmdUploadEnvironment string, cmdResourceTenantDomain string, cmdUsername, authToken string) {

	onPremKey = authToken
	token, preCommandErr := credentials.GetOAuthAccessToken(credential, cmdUploadEnvironment)
	accessToken = token
	if preCommandErr == nil {
		// getTenantsEnv(cmdUploadEnvironment)
		devPortalEndpoint := utils.GetDevPortalEndpointOfEnv(cmdUploadEnvironment, utils.MainConfigFilePath)
		headers := make(map[string]string)
		headers["API-KEY"] = onPremKey
		fmt.Println("Removing existing APIs..!")
		resp, err := utils.InvokeDELETERequest("http://localhost:9090/ai/spec-populator/bulk-remove", headers)

		if err != nil {
			return
		}

		if resp.StatusCode() == http.StatusOK {
			fmt.Println("Uploading APIs...")
			getTenants(devPortalEndpoint, "tenants?state=active&limit=100&offset=0")
		}
	} else {
		fmt.Println("Error getting OAuth Tokens : " + preCommandErr.Error())
	}
}

func getTenants(devPortalEndpoint, next string) {
	devPortalEndpoint = utils.AppendSlashToString(devPortalEndpoint)

	requestURL := devPortalEndpoint + next

	resp, err := InvokeGETRequest(requestURL, "")

	if err != nil {
		return
	}

	tenantListResponse := &utils.TenantListResponse{}
	unmarshalError := json.Unmarshal([]byte(resp.Body()), &tenantListResponse)

	if unmarshalError != nil {
		utils.HandleErrorAndExit(utils.LogPrefixError+"invalid JSON response", unmarshalError)
	}

	tenantCount := tenantListResponse.Pagination.Total

	if tenantCount == 0 {
		//handle carbon.super tenant
		tenants = append(tenants, utils.Tenant{
			Domain: utils.DefaultTenantDomain,
			Status: "ACTIVE"})
	} else {
		//handle all tenants
		tenants = tenantListResponse.List
	}

	// fmt.Println("Tenants: ", len(tenants))

	for i := 0; i < len(tenants); i++ {
		fmt.Println("\nuploading apis from tenant:", tenants[i].Domain)
		getAPIInfo(devPortalEndpoint, tenants[i].Domain, "apis?limit=50&offset=0")
	}

	if tenantListResponse.Pagination.Next != "" {
		getTenants(devPortalEndpoint, tenantListResponse.Pagination.Next)
	}
	time.Sleep(1 * time.Second)
}

func getAPIInfo(devPortalEndpoint, tenant, next string) {
	requestURL := devPortalEndpoint + next
	utils.Logln(utils.LogPrefixInfo+"ExportAPI: URL:", requestURL)
	headers := make(map[string]string)
	headers["x-wso2-tenant"] = tenant
	headers[utils.HeaderAccept] = utils.JsonArrayFormatType

	resp, err := utils.InvokeGETRequest(requestURL, headers)

	if err != nil {
		fmt.Println("Error in getting APIs: ", err)
	}

	apiListResponse := &utils.UploadAPIListResponse{}
	unmarshalError := json.Unmarshal([]byte(resp.Body()), &apiListResponse)

	if unmarshalError != nil {
		utils.HandleErrorAndExit(utils.LogPrefixError+"invalid JSON response", unmarshalError)
	}

	apiCount := apiListResponse.Pagination.Total

	if apiCount == 0 {
		fmt.Println("No APIs available to be uploaded..!")
	} else {
		upload(devPortalEndpoint, tenant, apiListResponse.List)
		fmt.Println("Successfully uploaded " + strconv.Itoa(len(apiListResponse.List)) + " APIs..!")
		if apiListResponse.Pagination.Next != "" {
			getAPIInfo(devPortalEndpoint, tenant, apiListResponse.Pagination.Next)
		}
	}
}

func upload(devPortalEndpoint string, tenant string, apiList []utils.UploadAPI) {
	payload := []map[string]string{}
	for i := 0; i < len(apiList); i++ {

		api := map[string]string{
			"uuid":          apiList[i].ID,
			"description":   apiList[i].Description,
			"api_name":      apiList[i].Name,
			"version":       apiList[i].Version,
			"tenant_domain": tenant,
			"api_type":      apiList[i].Type,
		}

		if apiList[i].Type == "HTTP" || apiList[i].Type == "APIPRODUCT" || apiList[i].Type == "REST" || apiList[i].Type == "SOAP" || apiList[i].Type == "SOAPTOREST" {
			requestURL := devPortalEndpoint + "apis/" + apiList[i].ID + "/swagger"
			resp, err := InvokeGETRequest(requestURL, tenant)
			if err != nil {
				fmt.Println("Error in sending request: ", err)
			}
			api["api_spec"] = resp.String()
		} else if apiList[i].Type == "GRAPHQL" {
			requestURL := devPortalEndpoint + "apis/" + apiList[i].ID + "/graphql-schema"
			resp, err := InvokeGETRequest(requestURL, tenant)
			if err != nil {
				fmt.Println("Error in sending request: ", err)
			}
			api["async_spec"] = resp.String()
		} else if apiList[i].Type == "WS" || apiList[i].Type == "WEBSUB" || apiList[i].Type == "ASYNC" || apiList[i].Type == "SSE" || apiList[i].Type == "WEBHOOK" {
			requestURL := devPortalEndpoint + "apis/" + apiList[i].ID + "/async-api-specification"
			resp, err := InvokeGETRequest(requestURL, tenant)
			if err != nil {
				fmt.Println("Error in sending request: ", err)
			}
			api["sdl_schema"] = resp.String()
		} else {
			continue
		}
		payload = append(payload, api)
	}

	InvokePOSTRequest(payload)
}

func InvokeGETRequest(requestURL, tenant string) (*resty.Response, error) {
	utils.Logln(utils.LogPrefixInfo+"ExportAPI: URL:", requestURL)
	headers := make(map[string]string)
	headers[utils.HeaderAuthorization] = utils.HeaderValueAuthBearerPrefix + " " + accessToken
	headers["x-wso2-tenant"] = tenant
	headers[utils.HeaderAccept] = utils.JsonArrayFormatType

	return utils.InvokeGETRequest(requestURL, headers)
}

func InvokePOSTRequest(payload []map[string]string) {
	fmt.Println("Sending post request..!")
	go func(payload []map[string]string) {
		jsonData, err := json.Marshal(map[string]interface{}{"apis": payload})
		if err != nil {
			log.Fatal(err)
		}
		headers := make(map[string]string)
		headers["API-KEY"] = onPremKey
		headers[utils.HeaderContentType] = utils.HeaderValueApplicationJSON

		resp, err := utils.InvokePOSTRequest("http://localhost:9090/ai/spec-populator/bulk-upload", headers, jsonData)

		if err != nil {
			utils.HandleErrorAndExit("API upload failed. Reason: ", err)
		}

		fmt.Println("Response:", string(resp.Body()))
	}(payload)

}
