// Package manifests deals with creating manifests for all manifests to be installed for the cluster
package manifests

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"text/template"

	"github.com/ghodss/yaml"
	"github.com/pkg/errors"

	"github.com/openshift/installer/pkg/asset"
	"github.com/openshift/installer/pkg/asset/installconfig"
	"github.com/openshift/installer/pkg/asset/manifests/content/bootkube"
	"github.com/openshift/installer/pkg/asset/tls"
)

const (
	manifestDir = "manifests"
)

var (
	kubeSysConfigPath = filepath.Join(manifestDir, "cluster-config.yaml")

	_ asset.WritableAsset = (*Manifests)(nil)
)

// Manifests generates the dependent operator config.yaml files
type Manifests struct {
	KubeSysConfig *configurationObject
	FileList      []*asset.File
}

type genericData map[string]string

// Name returns a human friendly name for the operator
func (m *Manifests) Name() string {
	return "Common Manifests"
}

// Dependencies returns all of the dependencies directly needed by a
// Manifests asset.
func (m *Manifests) Dependencies() []asset.Asset {
	return []asset.Asset{
		&installconfig.InstallConfig{},
		&networkOperator{},
		&tls.RootCA{},
		&tls.EtcdCA{},
		&tls.IngressCertKey{},
		&tls.KubeCA{},
		&tls.ServiceServingCA{},
		&tls.EtcdClientCertKey{},
		&tls.MCSCertKey{},
		&tls.KubeletCertKey{},
	}
}

// Generate generates the respective operator config.yml files
func (m *Manifests) Generate(dependencies asset.Parents) error {
	no := &networkOperator{}
	installConfig := &installconfig.InstallConfig{}
	dependencies.Get(no, installConfig)

	// no+mao go to kube-system config map
	m.KubeSysConfig = configMap("kube-system", "cluster-config-v1", genericData{
		"network-config": string(no.Files()[0].Data),
		"install-config": string(installConfig.Files()[0].Data),
	})
	kubeSysConfigData, err := yaml.Marshal(m.KubeSysConfig)
	if err != nil {
		return errors.Wrap(err, "failed to create kube-system/cluster-config-v1 configmap")
	}

	m.FileList = []*asset.File{
		{
			Filename: kubeSysConfigPath,
			Data:     kubeSysConfigData,
		},
	}
	m.FileList = append(m.FileList, m.generateBootKubeManifests(dependencies)...)

	return nil
}

// Files returns the files generated by the asset.
func (m *Manifests) Files() []*asset.File {
	return m.FileList
}

func (m *Manifests) generateBootKubeManifests(dependencies asset.Parents) []*asset.File {
	installConfig := &installconfig.InstallConfig{}
	etcdCA := &tls.EtcdCA{}
	kubeCA := &tls.KubeCA{}
	mcsCertKey := &tls.MCSCertKey{}
	etcdClientCertKey := &tls.EtcdClientCertKey{}
	rootCA := &tls.RootCA{}
	serviceServingCA := &tls.ServiceServingCA{}
	dependencies.Get(
		installConfig,
		etcdCA,
		etcdClientCertKey,
		kubeCA,
		mcsCertKey,
		rootCA,
		serviceServingCA,
	)

	etcdEndpointHostnames := make([]string, installConfig.Config.MasterCount())
	for i := range etcdEndpointHostnames {
		etcdEndpointHostnames[i] = fmt.Sprintf("%s-etcd-%d", installConfig.Config.ObjectMeta.Name, i)
	}

	templateData := &bootkubeTemplateData{
		Base64encodeCloudProviderConfig: "", // FIXME
		EtcdCaCert:                      base64.StdEncoding.EncodeToString(etcdCA.Cert()),
		EtcdClientCert:                  base64.StdEncoding.EncodeToString(etcdClientCertKey.Cert()),
		EtcdClientKey:                   base64.StdEncoding.EncodeToString(etcdClientCertKey.Key()),
		KubeCaCert:                      base64.StdEncoding.EncodeToString(kubeCA.Cert()),
		KubeCaKey:                       base64.StdEncoding.EncodeToString(kubeCA.Key()),
		McsTLSCert:                      base64.StdEncoding.EncodeToString(mcsCertKey.Cert()),
		McsTLSKey:                       base64.StdEncoding.EncodeToString(mcsCertKey.Key()),
		PullSecret:                      base64.StdEncoding.EncodeToString([]byte(installConfig.Config.PullSecret)),
		RootCaCert:                      base64.StdEncoding.EncodeToString(rootCA.Cert()),
		ServiceServingCaCert:            base64.StdEncoding.EncodeToString(serviceServingCA.Cert()),
		ServiceServingCaKey:             base64.StdEncoding.EncodeToString(serviceServingCA.Key()),
		TectonicNetworkOperatorImage:    "quay.io/coreos/tectonic-network-operator-dev:375423a332f2c12b79438fc6a6da6e448e28ec0f",
		CVOClusterID:                    installConfig.Config.ClusterID,
		EtcdEndpointHostnames:           etcdEndpointHostnames,
		EtcdEndpointDNSSuffix:           installConfig.Config.BaseDomain,
	}

	assetData := map[string][]byte{
		"kube-cloud-config.yaml":                     applyTemplateData(bootkube.KubeCloudConfig, templateData),
		"machine-config-server-tls-secret.yaml":      applyTemplateData(bootkube.MachineConfigServerTLSSecret, templateData),
		"openshift-service-signer-secret.yaml":       applyTemplateData(bootkube.OpenshiftServiceCertSignerSecret, templateData),
		"pull.json":                                  applyTemplateData(bootkube.Pull, templateData),
		"tectonic-network-operator.yaml":             applyTemplateData(bootkube.TectonicNetworkOperator, templateData),
		"cvo-overrides.yaml":                         applyTemplateData(bootkube.CVOOverrides, templateData),
		"legacy-cvo-overrides.yaml":                  applyTemplateData(bootkube.LegacyCVOOverrides, templateData),
		"etcd-service-endpoints.yaml":                applyTemplateData(bootkube.EtcdServiceEndpointsKubeSystem, templateData),
		"kube-system-configmap-etcd-serving-ca.yaml": applyTemplateData(bootkube.KubeSystemConfigmapEtcdServingCA, templateData),
		"kube-system-configmap-root-ca.yaml":         applyTemplateData(bootkube.KubeSystemConfigmapRootCA, templateData),
		"kube-system-secret-etcd-client.yaml":        applyTemplateData(bootkube.KubeSystemSecretEtcdClient, templateData),

		"01-tectonic-namespace.yaml":                 []byte(bootkube.TectonicNamespace),
		"03-openshift-web-console-namespace.yaml":    []byte(bootkube.OpenshiftWebConsoleNamespace),
		"04-openshift-machine-config-operator.yaml":  []byte(bootkube.OpenshiftMachineConfigOperator),
		"05-openshift-cluster-api-namespace.yaml":    []byte(bootkube.OpenshiftClusterAPINamespace),
		"09-openshift-service-signer-namespace.yaml": []byte(bootkube.OpenshiftServiceCertSignerNamespace),
		"app-version-kind.yaml":                      []byte(bootkube.AppVersionKind),
		"app-version-tectonic-network.yaml":          []byte(bootkube.AppVersionTectonicNetwork),
		"etcd-service.yaml":                          []byte(bootkube.EtcdServiceKubeSystem),
	}

	files := make([]*asset.File, 0, len(assetData))
	for name, data := range assetData {
		files = append(files, &asset.File{
			Filename: filepath.Join(manifestDir, name),
			Data:     data,
		})
	}

	return files
}

func applyTemplateData(template *template.Template, templateData interface{}) []byte {
	buf := &bytes.Buffer{}
	if err := template.Execute(buf, templateData); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// Load returns the manifests asset from disk.
func (m *Manifests) Load(f asset.FileFetcher) (bool, error) {
	fileList, err := f.FetchByPattern(filepath.Join(manifestDir, "*"))
	if err != nil {
		return false, err
	}
	if len(fileList) == 0 {
		return false, nil
	}

	kubeSysConfig := &configurationObject{}
	var found bool
	for _, file := range fileList {
		if file.Filename == kubeSysConfigPath {
			if err := yaml.Unmarshal(file.Data, kubeSysConfig); err != nil {
				return false, errors.Wrapf(err, "failed to unmarshal cluster-config.yaml")
			}
			found = true
		}
	}

	if !found {
		return false, nil

	}

	m.FileList, m.KubeSysConfig = fileList, kubeSysConfig

	return true, nil
}
