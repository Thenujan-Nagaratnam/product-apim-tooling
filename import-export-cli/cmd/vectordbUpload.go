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

package cmd

import (
	"github.com/spf13/cobra"
	"github.com/wso2/product-apim-tooling/import-export-cli/credentials"
	"github.com/wso2/product-apim-tooling/import-export-cli/impl"
	"github.com/wso2/product-apim-tooling/import-export-cli/utils"
)

// Purge command related usage Info
 const UploadCmdLiteral = "vector-db-upload"
 const UploadCmdShortDesc = "Upload APIs and API Products available in an environment to the vector database."
 const UploadCmdLongDesc = `Upload APIs and API Products available in the environment specified by flag (--environment, -e)`
 const UploadCmdExamples = utils.ProjectName + ` ` + UploadCmdLiteral + ` ` + UploadCmdLiteral + ` --token <on-prem-key> --endpoint <endpoint> -e dev`
 
var token string
var endpoint string

var UploadCmd = &cobra.Command{
	Use: UploadCmdLiteral + " (--endpoint <endpoint-url> --token <on-prem-key-of-the-organization> --environment " +
		"<environment-from-which-artifacts-should-be-Uploaded>)",
	Short:   UploadCmdShortDesc,
	Long:    UploadCmdLongDesc,
	Example: UploadCmdExamples,
	Run: func(cmd *cobra.Command, args []string) {
		utils.Logln(utils.LogPrefixInfo + UploadCmdLiteral + " called")

		cred, err := GetCredentials(CmdUploadEnvironment)
		if err != nil {
			utils.HandleErrorAndExit("Error getting credentials", err)
		}
		executeUploadAPIsCmd(cred, token, endpoint)
	},
}

// Do operations to Upload APIs to the vector database
func executeUploadAPIsCmd(credential credentials.Credential, token, endpoint string) {
	impl.UploadAPIs(credential, CmdUploadEnvironment, token, endpoint)
}

func init() {
	RootCmd.AddCommand(UploadCmd)
	UploadCmd.Flags().StringVarP(&CmdUploadEnvironment, "environment", "e",
		"", "Environment from which the APIs should be Uploaded")
	UploadCmd.Flags().StringVarP(&token, "token", "", "", "on-prem-key of the organization")
	UploadCmd.Flags().StringVarP(&endpoint, "endpoint", "", "", "endpoint of the marketplace assistant service")
	_ = UploadCmd.MarkFlagRequired("environment")
	_ = UploadCmd.MarkFlagRequired("token")
	_ = UploadCmd.MarkFlagRequired("endpoint")
}
