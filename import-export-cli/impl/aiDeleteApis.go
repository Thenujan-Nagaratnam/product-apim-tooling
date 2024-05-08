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
	"fmt"

	"github.com/wso2/product-apim-tooling/import-export-cli/credentials"
	"github.com/wso2/product-apim-tooling/import-export-cli/utils"
)
 
func PurgeAPIs(credential credentials.Credential, cmdUsername, authToken, endpointUrl string) {

	 onPremKey = authToken
	 endpoint = endpointUrl
  
	 fmt.Println("Removing existing APIs from vector DB..!")
	 err := RemoveExistingAPIs()
	 if err != nil {
		 utils.HandleErrorAndExit("Error in removing existing APIs", err)
	 }
}
