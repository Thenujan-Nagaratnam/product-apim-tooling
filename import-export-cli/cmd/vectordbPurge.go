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
const PurgeCmdLiteral = "vector-db-purge"
const PurgeCmdShortDesc = "Purge APIs and API Products available in an environment from the vector database."
const PurgeCmdLongDesc = `Purge APIs and API Products available in the environment specified by flag (--environment, -e)`
const PurgeCmdExamples = utils.ProjectName + ` ` + PurgeCmdLiteral + ` ` + PurgeCmdLiteral + ` --token <on-prem-key> --endpoint <endpoint> -e dev`

var PurgeCmd = &cobra.Command{
	Use: PurgeCmdLiteral + " (--endpoint <endpoint-url> --token <on-prem-key-of-the-organization> --environment " +
		"<environment-from-which-artifacts-should-be-purgeed>)",
	Short:   PurgeCmdShortDesc,
	Long:    PurgeCmdLongDesc,
	Example: PurgeCmdExamples,
	Run: func(cmd *cobra.Command, args []string) {
		utils.Logln(utils.LogPrefixInfo + PurgeCmdLiteral + " called")

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
	RootCmd.AddCommand(PurgeCmd)
	PurgeCmd.Flags().StringVarP(&CmdPurgeEnvironment, "environment", "e",
		"", "Environment from which the APIs should be Purgeed")
	PurgeCmd.Flags().StringVarP(&token, "token", "", "", "on-prem-key of the organization")
	PurgeCmd.Flags().StringVarP(&endpoint, "endpoint", "", "", "endpoint of the marketplace assistant service")
	_ = PurgeCmd.MarkFlagRequired("environment")
	_ = PurgeCmd.MarkFlagRequired("token")
	_ = PurgeCmd.MarkFlagRequired("endpoint")
}
