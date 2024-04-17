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

const uploadAPIsCmdLongDesc = "Upload APIs/API Products in an environment to a vector database for providing context to the marketplace assistant."
const UploadAPIsCmdLongDesc = `Upload APIs and API Products available in the environment specified by flag (--environment, -e)`
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

// Do operations to upload APIs for the migration into the directory passed as UploadDirectory
// <upload_directory> is the patch defined in main_config.yaml
// uploadDirectory = <upload_directory>/migration/
func executeUploadAPIsCmd(credential credentials.Credential, token string) {

	// exportRelatedFilesPath := filepath.Join(exportDirectory, CmdUploadEnvironment,
	// 	utils.GetMigrationExportTenantDirName(CmdResourceTenantDomain))
	// //e.g. /home/samithac/.wso2apictl/exported/migration/production-2.5/wso2-dot-org
	// startFromBeginning = true
	// isProcessCompleted = false

	// apiListOffset = 0
	// startingApiIndexFromList = 0
	// count, apis = getAPIList(credential, CmdUploadEnvironment, CmdResourceTenantDomain)

	// // impl.PrepareStartFromBeginning(credential, exportRelatedFilesPath, CmdResourceTenantDomain, CmdUsername, CmdUploadEnvironment)

	// fmt.Println("Uploading APIs for the marketplace assistant...")

	impl.UploadAPIs(credential, CmdUploadEnvironment, CmdResourceTenantDomain, CmdUsername, token)
}

// // Get the list of APIs from the defined offset index, upto the limit of constant value utils.MaxAPIsToExportOnce
// func getAPIList(credential credentials.Credential, cmdExportEnvironment, cmdResourceTenantDomain string) (count int32, apis []utils.API) {
// 	accessToken, preCommandErr := credentials.GetOAuthAccessToken(credential, cmdExportEnvironment)
// 	if preCommandErr == nil {
// 		apiListEndpoint := utils.GetApiListEndpointOfEnv(cmdExportEnvironment, utils.MainConfigFilePath)
// 		apiListEndpoint += "?limit=" + strconv.Itoa(utils.MaxAPIsToExportOnce) + "&offset=" + strconv.Itoa(apiListOffset)
// 		if cmdResourceTenantDomain != "" {
// 			apiListEndpoint += "&tenantDomain=" + cmdResourceTenantDomain
// 		}
// 		count, apis, err := impl.GetAPIList(accessToken, apiListEndpoint, "", "")
// 		if err == nil {
// 			return count, apis
// 		} else {
// 			utils.HandleErrorAndExit(utils.LogPrefixError+"Getting List of APIs.", utils.GetHttpErrorResponse(err))
// 		}
// 	} else {
// 		utils.HandleErrorAndExit(utils.LogPrefixError+"Error in getting access token for user while getting "+
// 			"the list of APIs: ", preCommandErr)
// 	}
// 	return 0, nil
// }

func init() {
	UploadCmd.AddCommand(UploadAPIsCmd)
	UploadAPIsCmd.Flags().StringVarP(&CmdUploadEnvironment, "environment", "e",
		"", "Environment from which the APIs should be uploaded")
	UploadAPIsCmd.Flags().StringVarP(&token, "token", "", "", "on-prem-key of the organization")
	_ = UploadAPIsCmd.MarkFlagRequired("environment")
	_ = UploadAPIsCmd.MarkFlagRequired("token")
}
