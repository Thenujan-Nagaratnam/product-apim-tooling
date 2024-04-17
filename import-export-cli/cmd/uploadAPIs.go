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

const UploadAPIsCmdLiteral = "apis"
const uploadAPIsCmdShortDesc = "Upload APIs for migration"

const uploadAPIsCmdLongDesc = "Upload public APIs and API Products in an environment to a vector database to provide context to the marketplace assistant."
const UploadAPIsCmdLongDesc = `Upload public APIs and API Products available in the environment specified by flag (--environment, -e)`
const uploadAPIsCmdExamples = utils.ProjectName + ` ` + UploadCmdLiteral + ` ` + UploadAPIsCmdLiteral + ` --token <on-prem-key> -e dev`

var token string

var UploadAPIsCmd = &cobra.Command{
	Use: UploadAPIsCmdLiteral + " (--token <on-prem-key-of-the-organization> --environment " +
		"<environment-from-which-artifacts-should-be-uploaded>)",
	Short:   uploadAPIsCmdShortDesc,
	Long:    uploadAPIsCmdLongDesc,
	Example: uploadAPIsCmdExamples,
	Run: func(cmd *cobra.Command, args []string) {
		utils.Logln(utils.LogPrefixInfo + UploadAPIsCmdLiteral + " called")

		cred, err := GetCredentials(CmdUploadEnvironment)
		if err != nil {
			utils.HandleErrorAndExit("Error getting credentials", err)
		}
		executeUploadAPIsCmd(cred, token)
	},
}

// Do operations to upload APIs to the vector database
func executeUploadAPIsCmd(credential credentials.Credential, token string) {
	impl.UploadAPIs(credential, CmdUploadEnvironment, CmdResourceTenantDomain, CmdUsername, token)
}

func init() {
	UploadCmd.AddCommand(UploadAPIsCmd)
	UploadAPIsCmd.Flags().StringVarP(&CmdUploadEnvironment, "environment", "e",
		"", "Environment from which the APIs should be uploaded")
	UploadAPIsCmd.Flags().StringVarP(&token, "token", "", "", "on-prem-key of the organization")
	_ = UploadAPIsCmd.MarkFlagRequired("environment")
	_ = UploadAPIsCmd.MarkFlagRequired("token")
}
