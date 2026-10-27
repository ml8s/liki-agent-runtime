package a2a

import (
	"github.com/a2aproject/a2a-go/v2/a2a"
)

func securitySchemes(config SecurityConfig) a2a.NamedSecuritySchemes {
	schemes := a2a.NamedSecuritySchemes{}
	if config.Bearer {
		schemes["agent_runtime_service_bearer"] = a2a.HTTPAuthSecurityScheme{
			Scheme:       "Bearer",
			BearerFormat: "opaque",
			Description:  "Runtime internal service bearer token",
		}
	}
	if config.MutualTLS {
		schemes["agent_runtime_service_mtls"] = a2a.MutualTLSSecurityScheme{
			Description: "Runtime verified client-certificate channel",
		}
	}
	return schemes
}

func securityRequirements(config SecurityConfig) a2a.SecurityRequirementsOptions {
	var options a2a.SecurityRequirementsOptions
	if config.Bearer {
		options = append(options, a2a.SecurityRequirements{"agent_runtime_service_bearer": {}})
	}
	if config.MutualTLS {
		options = append(options, a2a.SecurityRequirements{"agent_runtime_service_mtls": {}})
	}
	return options
}
