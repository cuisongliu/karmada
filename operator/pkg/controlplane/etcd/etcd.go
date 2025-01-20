/*
Copyright 2023 The Karmada Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package etcd

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kuberuntime "k8s.io/apimachinery/pkg/runtime"
	clientset "k8s.io/client-go/kubernetes"
	clientsetscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/component-base/cli/flag"

	operatorv1alpha1 "github.com/karmada-io/karmada/operator/pkg/apis/operator/v1alpha1"
	"github.com/karmada-io/karmada/operator/pkg/constants"
	"github.com/karmada-io/karmada/operator/pkg/util"
	"github.com/karmada-io/karmada/operator/pkg/util/apiclient"
	"github.com/karmada-io/karmada/operator/pkg/util/patcher"
)

// EnsureKarmadaEtcd creates etcd StatefulSet and service resource.
func EnsureKarmadaEtcd(client clientset.Interface, cfg *operatorv1alpha1.LocalEtcd, name, namespace string) error {
	if err := installKarmadaEtcd(client, name, namespace, cfg); err != nil {
		return err
	}
	return createEtcdService(client, name, namespace)
}

func installKarmadaEtcd(client clientset.Interface, name, namespace string, cfg *operatorv1alpha1.LocalEtcd) error {
	// if the number of etcd is greater than one, we need to concatenate the peerURL for each member cluster.
	// memberName is podName generated by etcd statefulset: ${statefulsetName}-index
	// memberPeerURL uses the etcd peer headless service name: ${podName}.${serviceName}.${namespace}.svc.cluster.local:2380
	initialClusters := make([]string, *cfg.Replicas)
	for index := range initialClusters {
		memberName := fmt.Sprintf("%s-%d", util.KarmadaEtcdName(name), index)

		// build etcd member cluster peer url
		memberPeerURL := fmt.Sprintf("http://%s.%s.%s.svc.cluster.local:%v",
			memberName,
			util.KarmadaEtcdName(name),
			namespace,
			constants.EtcdListenPeerPort,
		)

		initialClusters[index] = fmt.Sprintf("%s=%s", memberName, memberPeerURL)
	}

	etcdStatefulSetBytes, err := util.ParseTemplate(KarmadaEtcdStatefulSet, struct {
		StatefulSetName, Namespace, Image, ImagePullPolicy, EtcdClientService string
		CertsSecretName, EtcdPeerServiceName                                  string
		InitialCluster, EtcdDataVolumeName, EtcdCipherSuites                  string
		Replicas, EtcdListenClientPort, EtcdListenPeerPort                    int32
	}{
		StatefulSetName:      util.KarmadaEtcdName(name),
		Namespace:            namespace,
		Image:                cfg.Image.Name(),
		ImagePullPolicy:      string(cfg.ImagePullPolicy),
		EtcdClientService:    util.KarmadaEtcdClientName(name),
		CertsSecretName:      util.EtcdCertSecretName(name),
		EtcdPeerServiceName:  util.KarmadaEtcdName(name),
		EtcdDataVolumeName:   constants.EtcdDataVolumeName,
		InitialCluster:       strings.Join(initialClusters, ","),
		EtcdCipherSuites:     genEtcdCipherSuites(),
		Replicas:             *cfg.Replicas,
		EtcdListenClientPort: constants.EtcdListenClientPort,
		EtcdListenPeerPort:   constants.EtcdListenPeerPort,
	})
	if err != nil {
		return fmt.Errorf("error when parsing Etcd statefuelset template: %w", err)
	}

	etcdStatefulSet := &appsv1.StatefulSet{}
	if err := kuberuntime.DecodeInto(clientsetscheme.Codecs.UniversalDecoder(), etcdStatefulSetBytes, etcdStatefulSet); err != nil {
		return fmt.Errorf("error when decoding Etcd StatefulSet: %w", err)
	}

	patcher.NewPatcher().WithAnnotations(cfg.Annotations).WithLabels(cfg.Labels).
		WithVolumeData(cfg.VolumeData).WithResources(cfg.Resources).ForStatefulSet(etcdStatefulSet)

	if err := apiclient.CreateOrUpdateStatefulSet(client, etcdStatefulSet); err != nil {
		return fmt.Errorf("error when creating Etcd statefulset, err: %w", err)
	}

	return nil
}

func createEtcdService(client clientset.Interface, name, namespace string) error {
	etcdServicePeerBytes, err := util.ParseTemplate(KarmadaEtcdPeerService, struct {
		ServiceName, Namespace                   string
		EtcdListenClientPort, EtcdListenPeerPort int32
	}{
		ServiceName:          util.KarmadaEtcdName(name),
		Namespace:            namespace,
		EtcdListenClientPort: constants.EtcdListenClientPort,
		EtcdListenPeerPort:   constants.EtcdListenPeerPort,
	})
	if err != nil {
		return fmt.Errorf("error when parsing Etcd client serive template: %w", err)
	}

	etcdPeerService := &corev1.Service{}
	if err := kuberuntime.DecodeInto(clientsetscheme.Codecs.UniversalDecoder(), etcdServicePeerBytes, etcdPeerService); err != nil {
		return fmt.Errorf("error when decoding Etcd client service: %w", err)
	}

	if err := apiclient.CreateOrUpdateService(client, etcdPeerService); err != nil {
		return fmt.Errorf("error when creating etcd client service, err: %w", err)
	}

	etcdClientServiceBytes, err := util.ParseTemplate(KarmadaEtcdClientService, struct {
		ServiceName, Namespace string
		EtcdListenClientPort   int32
	}{
		ServiceName:          util.KarmadaEtcdClientName(name),
		Namespace:            namespace,
		EtcdListenClientPort: constants.EtcdListenClientPort,
	})
	if err != nil {
		return fmt.Errorf("error when parsing Etcd client serive template: %w", err)
	}

	etcdClientService := &corev1.Service{}
	if err := kuberuntime.DecodeInto(clientsetscheme.Codecs.UniversalDecoder(), etcdClientServiceBytes, etcdClientService); err != nil {
		return fmt.Errorf("err when decoding Etcd client service: %w", err)
	}

	if err := apiclient.CreateOrUpdateService(client, etcdClientService); err != nil {
		return fmt.Errorf("err when creating etcd client service, err: %w", err)
	}

	return nil
}

// Setting Golang's secure cipher suites as etcd's cipher suites.
// They are obtained by the return value of the function CipherSuites() under the go/src/crypto/tls/cipher_suites.go package.
// Consistent with the Preferred values of k8s’s default cipher suites.
func genEtcdCipherSuites() string {
	return strings.Join(flag.PreferredTLSCipherNames(), ",")
}
