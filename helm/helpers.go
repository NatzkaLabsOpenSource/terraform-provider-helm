package helm

import (
	"fmt"
	"log"
	"net/url"
	"sync"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/registry"
)

var k8sPrefix = "kubernetes.0."

type overridableData struct {
	providerData *schema.ResourceData
	override     dataGetter
}

// GetHelmConfiguration will return a new Helm configuration
func (m *Meta) GetHelmConfiguration(d dataGetter, namespace string) (*action.Configuration, error) {
	m.Lock()
	defer m.Unlock()
	debug("[INFO] GetHelmConfiguration start")
	actionConfig := new(action.Configuration)

	kc, err := newKubeConfig(&overridableData{
		providerData: m.data,
		override:     d,
	}, &namespace)
	if err != nil {
		return nil, err
	}

	if err := actionConfig.Init(kc, namespace, m.HelmDriver, debug); err != nil {
		return nil, err
	}

	debug("[INFO] GetHelmConfiguration success")
	return actionConfig, nil
}

// dataGetter lets us call Get methods on both schema.ResourceDiff and schema.ResourceData
type dataGetter interface {
	Get(key string) interface{}
	GetOk(string) (interface{}, bool)
	GetOkExists(string) (interface{}, bool)
}

// loggedInOCIRegistries is used to make sure we log into a registry only
// once if it is used across multiple resources concurrently
var loggedInOCIRegistries map[string]string = map[string]string{}
var OCILoginMutex sync.Mutex

// OCIRegistryLogin creates an OCI registry client and logs into the registry if needed
func OCIRegistryLogin(actionConfig *action.Configuration, d dataGetter) error {
	registryClient, err := registry.NewClient()
	if err != nil {
		return fmt.Errorf("could not create OCI registry client: %v", err)
	}
	actionConfig.RegistryClient = registryClient

	// log in to the registry if neccessary
	repository := d.Get("repository").(string)
	chartName := d.Get("chart").(string)
	var ociURL string
	if registry.IsOCI(repository) {
		ociURL = repository
	} else if registry.IsOCI(chartName) {
		ociURL = chartName
	}
	if ociURL == "" {
		return nil
	}

	username := d.Get("repository_username").(string)
	password := d.Get("repository_password").(string)
	if username != "" && password != "" {
		u, err := url.Parse(ociURL)
		if err != nil {
			return fmt.Errorf("could not parse OCI registry URL: %v", err)
		}

		OCILoginMutex.Lock()
		defer OCILoginMutex.Unlock()
		if _, ok := loggedInOCIRegistries[u.Host]; ok {
			debug("[INFO] Already logged into OCI registry %q", u.Host)
			return nil
		}
		err = registryClient.Login(u.Host,
			registry.LoginOptBasicAuth(username, password))
		if err != nil {
			return fmt.Errorf("could not login to OCI registry %q: %v", u.Host, err)
		}
		loggedInOCIRegistries[u.Host] = ""
		debug("[INFO] Logged into OCI registry")
	}

	return nil
}

func debug(format string, a ...interface{}) {
	log.Printf("[DEBUG] %s", fmt.Sprintf(format, a...))
}

func (o *overridableData) Get(key string) interface{} {
	if data, ok := o.override.GetOk(key); ok {
		return data
	}

	return o.providerData.Get(key)
}

func (o *overridableData) GetOk(key string) (interface{}, bool) {
	if data, ok := o.override.GetOk(key); ok {
		return data, true
	}

	return o.providerData.GetOk(key)
}

func (o *overridableData) GetOkExists(key string) (interface{}, bool) {
	if data, ok := o.override.GetOkExists(key); ok {
		return data, true
	}

	return o.providerData.GetOkExists(key)
}

func k8sGetOk(d dataGetter, key string) (interface{}, bool) {
	value, ok := d.GetOk(k8sPrefix + key)

	// For boolean attributes the zero value is Ok
	switch value.(type) {
	case bool:
		// TODO: replace deprecated GetOkExists with SDK v2 equivalent
		// https://github.com/hashicorp/terraform-plugin-sdk/pull/350
		value, ok = d.GetOkExists(k8sPrefix + key)
	}

	// fix: DefaultFunc is not being triggered on TypeList
	s := kubernetesResource().Schema[key]
	if !ok && s.DefaultFunc != nil {
		value, _ = s.DefaultFunc()

		switch v := value.(type) {
		case string:
			ok = len(v) != 0
		case bool:
			ok = v
		}
	}

	return value, ok
}

func expandStringSlice(s []interface{}) []string {
	result := make([]string, len(s), len(s))
	for k, v := range s {
		// Handle the Terraform parser bug which turns empty strings in lists to nil.
		if v == nil {
			result[k] = ""
		} else {
			result[k] = v.(string)
		}
	}
	return result
}
