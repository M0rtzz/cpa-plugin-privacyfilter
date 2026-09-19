package main

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var pluginVersion = "0.3.0"

func buildPlugin(configYAML []byte, pluginDir string) (pluginapi.Plugin, error) {
	cfg, errParse := parseConfig(configYAML)
	if errParse != nil {
		return pluginapi.Plugin{}, errParse
	}
	p := &privacyFilterPlugin{cfg: cfg}

	return pluginapi.Plugin{
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "rheodev",
			GitHubRepository: "https://github.com/M0rtzz/cpa-plugin-privacyfilter",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "allow_quoted_input",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Description: "Exempt the entire attributed input when its first non-whitespace character is > (default true).",
				},
				{
					Name:        "max_han_percent",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Description: "Maximum Han character percentage (0–100, default 20). A strictly higher ratio is rejected.",
				},
				{
					Name:        "skip_models",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "Model names exempt from language checks.",
				},
				{
					Name:        "skip_formats",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "Source formats exempt from language checks.",
				},
			},
		},
		Capabilities: pluginapi.Capabilities{
			RequestInterceptor: p,
		},
	}, nil
}
