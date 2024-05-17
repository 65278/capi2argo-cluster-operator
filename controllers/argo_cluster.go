// Package controllers implements functions for manipulating CAPI generated
// cluster secrets into Argo definitions.
package controllers

import (
	// b64 "encoding/base64"
	"encoding/json"
	// "errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	// ArgoNamespace represents the Namespace that hold ArgoCluster secrets.
	ArgoNamespace string
	// TestKubeConfig represents
	TestKubeConfig *rest.Config
)

const (
	metaAnnotations = iota
	metaLabels
)

const (
	clusterTakeAlongKeyFmt        = "take-along-%s.capi-to-argocd."
	clusterTakenFromClusterKeyFmt = "taken-from-cluster-%s.capi-to-argocd."
)

// GetArgoCommonLabels holds a map of labels that reconciled objects must have.
func GetArgoCommonLabels() map[string]string {
	return map[string]string{
		"capi-to-argocd/owned":           "true",
		"argocd.argoproj.io/secret-type": "cluster",
	}
}

// GetMetaType returns a struct of strings required to extract annotations and labels
func GetMetaType(metaType int) MetaType {
	var rv MetaType
	switch metaType {
	case metaAnnotations:
		rv.Name = "annotation"
	case metaLabels:
		rv.Name = "label"
	}
	rv.TakeAlong = fmt.Sprintf(clusterTakeAlongKeyFmt, rv.Name)
	rv.TakenFrom = fmt.Sprintf(clusterTakenFromClusterKeyFmt, rv.Name)
	return rv
}

// ArgoCluster holds all information needed for CAPI --> Argo Cluster conversion
type ArgoCluster struct {
	NamespacedName       types.NamespacedName
	ClusterName          string
	ClusterServer        string
	ClusterLabels        map[string]string
	TakeAlongAnnotations map[string]string
	TakeAlongLabels      map[string]string
	ClusterConfig        ArgoConfig
}

// ArgoConfig represents Argo Cluster.JSON.config
type ArgoConfig struct {
	TLSClientConfig *ArgoTLS `json:"tlsClientConfig,omitempty"`
	BearerToken     *string  `json:"bearerToken,omitempty"`
}

// MetaType holds info required to work with ObjectMeta annotations and labels
type MetaType struct {
	Name      string
	TakeAlong string
	TakenFrom string
}

// ArgoTLS represents Argo Cluster.JSON.config.tlsClientConfig
type ArgoTLS struct {
	CaData   *string `json:"caData,omitempty"`
	CertData *string `json:"certData,omitempty"`
	KeyData  *string `json:"keyData,omitempty"`
}

// NewArgoCluster return a new ArgoCluster
func NewArgoCluster(c *CapiCluster, s *corev1.Secret, cluster *clusterv1.Cluster) (*ArgoCluster, error) {
	log := ctrl.Log.WithName("argoCluster")

	takeAlongLabels := map[string]string{}
	var errList []string
	if cluster != nil {
		takeAlongLabels, errList = buildTakeAlongArray(cluster, metaLabels)
		for _, e := range errList {
			log.Info(e)
		}
	}
	takeAlongAnnotations := map[string]string{}
	if cluster != nil {
		takeAlongAnnotations, errList = buildTakeAlongArray(cluster, metaAnnotations)
		for _, e := range errList {
			log.Info(e)
		}
	}
	return &ArgoCluster{
		NamespacedName: BuildNamespacedName(s.ObjectMeta.Name, s.ObjectMeta.Namespace),
		ClusterName:    BuildClusterName(c.KubeConfig.Clusters[0].Name, s.ObjectMeta.Namespace),
		ClusterServer:  c.KubeConfig.Clusters[0].Cluster.Server,
		ClusterLabels: map[string]string{
			"capi-to-argocd/cluster-secret-name": c.Name + "-kubeconfig",
			"capi-to-argocd/cluster-namespace":   c.Namespace,
		},
		TakeAlongAnnotations: takeAlongAnnotations,
		TakeAlongLabels:      takeAlongLabels,
		ClusterConfig: ArgoConfig{
			BearerToken: c.KubeConfig.Users[0].User.Token,
			TLSClientConfig: &ArgoTLS{
				CaData:   &c.KubeConfig.Clusters[0].Cluster.CaData,
				CertData: c.KubeConfig.Users[0].User.CertData,
				KeyData:  c.KubeConfig.Users[0].User.KeyData,
			},
		},
	}, nil
}

// extractTakeAlongMeta returns the take-along label/annotation key from a cluster resource
func extractTakeAlongMeta(metaType string, key string) (string, error) {
	takeAlong := fmt.Sprintf(clusterTakeAlongKeyFmt, metaType)
	if strings.HasPrefix(key, takeAlong) {
		splitResult := strings.Split(key, takeAlong)
		if len(splitResult) >= 2 {
			if splitResult[1] != "" {
				return splitResult[1], nil
			}
		}
		return "", fmt.Errorf("invalid take-along %s. missing key after '/': %s", metaType, key)
	}
	// Not an take-along label. Return nil
	return "", nil
}

// buildTakeAlongArray returns a list of valid take-along metadata from a cluster metadata object
func buildTakeAlongArray(cluster *clusterv1.Cluster, metaType int) (map[string]string, []string) {
	name := cluster.Name
	namespace := cluster.Namespace
	var meta map[string]string
	metaName := GetMetaType(metaType)

	switch metaType {
	case metaAnnotations:
		meta = cluster.Annotations

	case metaLabels:
		meta = cluster.Labels

	default:
		return map[string]string{}, []string{}
	}

	takeAlongArray := []string{}

	for k := range meta {
		l, err := extractTakeAlongMeta(metaName.Name, k)
		if err != nil {
			return nil, []string{err.Error()}
		}
		if l != "" {
			takeAlongArray = append(takeAlongArray, l)
		}
	}

	takeAlongMap := make(map[string]string)

	errors := []string{}
	if len(takeAlongArray) > 0 {
		for _, key := range takeAlongArray {
			if key != "" {
				if _, ok := meta[key]; !ok {
					errors = append(errors, fmt.Sprintf("take-along %s '%s' not found on cluster resource: %s, namespace: %s. Ignoring", metaName.Name, key, name, namespace))
					continue
				}
				takeAlongMap[key] = meta[key]
				takeAlongMap[fmt.Sprintf("%s%s", metaName.TakenFrom, key)] = ""
			}
		}
	}

	return takeAlongMap, errors
}

// BuildNamespacedName returns k8s native object identifier.
func BuildNamespacedName(s string, namespace string) types.NamespacedName {
	return types.NamespacedName{
		Name:      "cluster-" + BuildClusterName(strings.TrimSuffix(s, "-kubeconfig"), namespace),
		Namespace: ArgoNamespace,
	}
}

// BuildClusterName returns cluster name after transformations applied (with/without namespace suffix, etc).
func BuildClusterName(s string, namespace string) string {
	prefix := ""
	if EnableNamespacedNames {
		prefix += namespace + "-"
	}
	return prefix + s
}

// ConvertToSecret converts an ArgoCluster into k8s native secret object.
func (a *ArgoCluster) ConvertToSecret() (*corev1.Secret, error) {
	// if err := ValidateClusterTLSConfig(&a.ClusterConfig.TLSClientConfig); err != nil {
	// 	return nil, err
	// }
	c, err := json.Marshal(a.ClusterConfig)
	if err != nil {
		return nil, err
	}

	mergedLabels := make(map[string]string)
	for key, value := range GetArgoCommonLabels() {
		mergedLabels[key] = value
	}
	for key, value := range a.ClusterLabels {
		mergedLabels[key] = value
	}
	for key, value := range a.TakeAlongLabels {
		mergedLabels[key] = value
	}

	argoSecret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      a.NamespacedName.Name,
			Namespace: a.NamespacedName.Namespace,
			Labels:    mergedLabels,
		},
		Data: map[string][]byte{
			"name":   []byte(a.ClusterName),
			"server": []byte(a.ClusterServer),
			"config": c,
		},
	}
	return argoSecret, nil
}

// ValidateClusterTLSConfig validates that we got proper based64 k/v fields.
// func ValidateClusterTLSConfig(a *ArgoTLS) error {
// 	for _, v := range []string{a.CaData, a.CertData, a.KeyData} {
// 		// Check if field.value is empty
// 		if v == "" {
// 			return errors.New("missing key on ArgoTLS config")
// 		}
// 		// Check if field.value is valid b64 encoded string
// 		if _, err := b64.StdEncoding.DecodeString(v); err != nil {
// 			return err
// 		}
// 	}
// 	return nil
// }
