package helm

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"

	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/helmpath"
	"helm.sh/helm/v3/pkg/storage/driver"

	// Import to initialize client auth plugins.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

// Meta is the meta information structure for the provider
type Meta struct {
	data       *schema.ResourceData
	Settings   *cli.EnvSettings
	HelmDriver string

	// Used to lock some operations
	sync.Mutex

	// Experimental feature toggles
	experiments map[string]bool
}

// Provider returns the provider schema to Terraform.
func Provider() *schema.Provider {
	p := &schema.Provider{
		Schema: map[string]*schema.Schema{
			"debug": {
				Type:        schema.TypeBool,
				Optional:    true,
				Description: "Debug indicates whether or not Helm is running in Debug mode.",
				DefaultFunc: schema.EnvDefaultFunc("HELM_DEBUG", false),
			},
			"plugins_path": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The path to the helm plugins directory",
				DefaultFunc: schema.EnvDefaultFunc("HELM_PLUGINS", helmpath.DataPath("plugins")),
			},
			"registry_config_path": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The path to the registry config file",
				DefaultFunc: schema.EnvDefaultFunc("HELM_REGISTRY_CONFIG", helmpath.ConfigPath("registry.json")),
			},
			"repository_config_path": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The path to the file containing repository names and URLs",
				DefaultFunc: schema.EnvDefaultFunc("HELM_REPOSITORY_CONFIG", helmpath.ConfigPath("repositories.yaml")),
			},
			"repository_cache": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The path to the file containing cached repository indexes",
				DefaultFunc: schema.EnvDefaultFunc("HELM_REPOSITORY_CACHE", helmpath.CachePath("repository")),
			},
			"helm_driver": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The backend storage driver. Values are: configmap, secret, memory, sql",
				DefaultFunc: schema.EnvDefaultFunc("HELM_DRIVER", "secret"),
				ValidateDiagFunc: func(val interface{}, key cty.Path) (diags diag.Diagnostics) {
					drivers := []string{
						strings.ToLower(driver.MemoryDriverName),
						strings.ToLower(driver.ConfigMapsDriverName),
						strings.ToLower(driver.SecretsDriverName),
						strings.ToLower(driver.SQLDriverName),
					}

					v := strings.ToLower(val.(string))

					for _, d := range drivers {
						if d == v {
							return
						}
					}
					return diag.Diagnostics{
						{
							Severity: diag.Error,
							Summary:  fmt.Sprintf("Invalid storage driver: %v used for helm_driver", v),
							Detail:   fmt.Sprintf("Helm backend storage driver must be set to one of the following values: %v", strings.Join(drivers, ", ")),
						},
					}
				},
			},
			"kubernetes": {
				Type:        schema.TypeList,
				MaxItems:    1,
				Optional:    true,
				Description: "Kubernetes configuration.",
				Elem:        kubernetesResource(),
			},
			"experiments": {
				Type:        schema.TypeList,
				MaxItems:    1,
				Optional:    true,
				Description: "Enable and disable experimental features.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"manifest": {
							Type:     schema.TypeBool,
							Optional: true,
							DefaultFunc: func() (interface{}, error) {
								if v := os.Getenv("TF_X_HELM_MANIFEST"); v != "" {
									vv, err := strconv.ParseBool(v)
									if err != nil {
										return false, err
									}
									return vv, nil
								}
								return false, nil
							},
							Description: "Enable full diff by storing the rendered manifest in the state.",
						},
					},
				},
			},
		},
		ResourcesMap: map[string]*schema.Resource{
			"helm_release": resourceRelease(),
		},
		DataSourcesMap: map[string]*schema.Resource{
			"helm_template": dataTemplate(),
		},
	}
	p.ConfigureContextFunc = func(ctx context.Context, d *schema.ResourceData) (interface{}, diag.Diagnostics) {
		return providerConfigure(d, p.TerraformVersion)
	}
	return p
}

func kubernetesResource() *schema.Resource {
	return &schema.Resource{
		Schema: map[string]*schema.Schema{
			"host": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("KUBE_HOST", ""),
				Description: "The hostname (in form of URI) of Kubernetes master.",
			},
			"username": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("KUBE_USER", ""),
				Description: "The username to use for HTTP basic authentication when accessing the Kubernetes master endpoint.",
			},
			"password": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("KUBE_PASSWORD", ""),
				Description: "The password to use for HTTP basic authentication when accessing the Kubernetes master endpoint.",
			},
			"insecure": {
				Type:        schema.TypeBool,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("KUBE_INSECURE", false),
				Description: "Whether server should be accessed without verifying the TLS certificate.",
			},
			"client_certificate": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("KUBE_CLIENT_CERT_DATA", ""),
				Description: "PEM-encoded client certificate for TLS authentication.",
			},
			"client_key": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("KUBE_CLIENT_KEY_DATA", ""),
				Description: "PEM-encoded client certificate key for TLS authentication.",
			},
			"cluster_ca_certificate": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("KUBE_CLUSTER_CA_CERT_DATA", ""),
				Description: "PEM-encoded root certificates bundle for TLS authentication.",
			},
			"config_paths": {
				Type:        schema.TypeList,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Optional:    true,
				Description: "A list of paths to kube config files. Can be set with KUBE_CONFIG_PATHS environment variable.",
			},
			"config_path": {
				Type:          schema.TypeString,
				Optional:      true,
				DefaultFunc:   schema.EnvDefaultFunc("KUBE_CONFIG_PATH", nil),
				Description:   "Path to the kube config file. Can be set with KUBE_CONFIG_PATH.",
				ConflictsWith: []string{"kubernetes.0.config_paths"},
			},
			"config_context": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("KUBE_CTX", ""),
			},
			"config_context_auth_info": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("KUBE_CTX_AUTH_INFO", ""),
				Description: "",
			},
			"config_context_cluster": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("KUBE_CTX_CLUSTER", ""),
				Description: "",
			},
			"token": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("KUBE_TOKEN", ""),
				Description: "Token to authenticate an service account",
			},
			"proxy_url": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "URL to the proxy to be used for all API requests",
				DefaultFunc: schema.EnvDefaultFunc("KUBE_PROXY_URL", ""),
			},
			"exec": {
				Type:     schema.TypeList,
				Optional: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"api_version": {
							Type:     schema.TypeString,
							Required: true,
							ValidateDiagFunc: func(val interface{}, key cty.Path) (diags diag.Diagnostics) {
								apiVersion := val.(string)
								if apiVersion == "client.authentication.k8s.io/v1alpha1" {
									return diag.Diagnostics{{
										Severity: diag.Warning,
										Summary:  "v1alpha1 of the client authentication API has been removed, use v1beta1 or above",
										Detail:   "v1alpha1 of the client authentication API is removed in Kubernetes client versions 1.24 and above. You may need to update your exec plugin to use the latest version.",
									}}
								}
								return
							},
						},
						"command": {
							Type:     schema.TypeString,
							Required: true,
						},
						"env": {
							Type:     schema.TypeMap,
							Optional: true,
							Elem:     &schema.Schema{Type: schema.TypeString},
						},
						"args": {
							Type:     schema.TypeList,
							Optional: true,
							Elem:     &schema.Schema{Type: schema.TypeString},
						},
					},
				},
				Description: "",
			},
		},
	}
}

func providerConfigure(d *schema.ResourceData, terraformVersion string) (interface{}, diag.Diagnostics) {
	m := &Meta{
		data: d,
		experiments: map[string]bool{
			"manifest": d.Get("experiments.0.manifest").(bool),
		},
	}

	log.Println("[DEBUG] Experiments enabled:", m.GetEnabledExperiments())

	settings := cli.New()
	settings.Debug = d.Get("debug").(bool)

	if v, ok := d.GetOk("plugins_path"); ok {
		settings.PluginsDirectory = v.(string)
	}

	if v, ok := d.GetOk("registry_config_path"); ok {
		settings.RegistryConfig = v.(string)
	}

	if v, ok := d.GetOk("repository_config_path"); ok {
		settings.RepositoryConfig = v.(string)
	}

	if v, ok := d.GetOk("repository_cache"); ok {
		settings.RepositoryCache = v.(string)
	}

	m.Settings = settings

	if v, ok := d.GetOk("helm_driver"); ok {
		m.HelmDriver = v.(string)
	}

	return m, nil
}

// ExperimentEnabled returns true it the named experiment is enabled
func (m *Meta) ExperimentEnabled(name string) bool {
	return m.experiments[name]
}

// GetEnabledExperiments returns a list of the experimental features that are enabled
func (m *Meta) GetEnabledExperiments() []string {
	enabled := []string{}
	for k, v := range m.experiments {
		if v {
			enabled = append(enabled, k)
		}
	}
	return enabled
}
