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

const PurgeAPIsCmdLiteral = "apis"
const purgeAPIsCmdShortDesc = "Purge APIs and API Products in an environment from a vector database."

const purgeAPIsCmdLongDesc = "Purge APIs and API Products in an environment from a vector database."
const PurgeAPIsCmdLongDesc = `Purge APIs and API Products available in the environment specified by flag (--environment, -e)`
const purgeAPIsCmdExamples = utils.ProjectName + ` ` + PurgeCmdLiteral + ` ` + PurgeAPIsCmdLiteral + ` --token 2fdca1b6-6a28-4aea-add6-77c97033bdb9 --endpoint https://dev-tools.wso2.com/apim-ai-service -e production
							NOTE: All the flags (--token, --endpoint and --environment (-e)) are mandatory`

var PurgeAPIsCmd = &cobra.Command{
	Use: PurgeAPIsCmdLiteral + " (--endpoint <endpoint-url> --token <on-prem-key-of-the-organization> --environment " +
		"<environment-from-which-artifacts-should-be-purgeed>)",
	Short:   purgeAPIsCmdShortDesc,
	Long:    purgeAPIsCmdLongDesc,
	Example: purgeAPIsCmdExamples,
	Run: func(cmd *cobra.Command, args []string) {
		utils.Logln(utils.LogPrefixInfo + PurgeAPIsCmdLiteral + " called")

		cred, err := GetCredentials(CmdPurgeEnvironment)
		if err != nil {
			utils.HandleErrorAndExit("Error getting credentials", err)
		}
		executePurgeAPIsCmd(cred, token, endpoint)
	},
}

// Do operations to Purge APIs to the vector database
func executePurgeAPIsCmd(credential credentials.Credential, token, endpoint string) {
	impl.PurgeAPIs(credential, CmdPurgeEnvironment, token, endpoint)
}

func init() {
	PurgeCmd.AddCommand(PurgeAPIsCmd)
	PurgeAPIsCmd.Flags().StringVarP(&CmdPurgeEnvironment, "environment", "e",
		"", "Environment from which the APIs should be Purgeed")
	PurgeAPIsCmd.Flags().StringVarP(&token, "token", "", "", "on-prem-key of the organization")
	PurgeAPIsCmd.Flags().StringVarP(&endpoint, "endpoint", "", "", "endpoint of the marketplace assistant service")
	_ = PurgeAPIsCmd.MarkFlagRequired("environment")
	_ = PurgeAPIsCmd.MarkFlagRequired("token")
	_ = PurgeAPIsCmd.MarkFlagRequired("endpoint")
}
