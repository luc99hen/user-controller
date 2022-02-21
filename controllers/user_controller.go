/*
Copyright 2021.

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

package controllers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/pkg/errors"
	certificatesv1 "k8s.io/api/certificates/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/certificate/csr"
	"k8s.io/client-go/util/keyutil"
	bootstraputil "k8s.io/cluster-bootstrap/token/util"
	"k8s.io/klog/v2"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	tokenphase "k8s.io/kubernetes/cmd/kubeadm/app/phases/bootstraptoken/node"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	appv1alpha1 "github.com/openyurtio/yurt-user-controller/controllers/yurt-app-manager/api/v1alpha1"
	//appv1alpha1 "github.com/openyurtio/yurt-app-manager/pkg/yurtappmanager/apis/apps/v1alpha1"
	userv1alpha1 "github.com/openyurtio/yurt-user-controller/api/v1alpha1"
)

// UserReconciler reconciles a User object
type UserReconciler struct {
	//cfg        *rest.Config
	apiClients clientset.Interface
	client.Client
	Scheme *runtime.Scheme
	UserValidDays int
}

const (
	// DefaultUserValidDays 用户有效期默认为5天
	DefaultUserValidDays = 5
	GenerateKubeConfigErr       = "GenerateKubeConfigErr"
	GenerateJoinTokenErr        = "GenerateJoinTokenErr"
	GenerateNamespaceErr        = "GenerateNamespaceErr"
)

//+kubebuilder:rbac:groups=user.openyurt.io,resources=users,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=user.openyurt.io,resources=users/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=user.openyurt.io,resources=users/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the User object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *UserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var user userv1alpha1.User
	if err := r.Get(ctx, req.NamespacedName, &user); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// 1. Handle the User deletion event
	if !user.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &user)
	}
	originUser := user.DeepCopy()
	updateErrs := r.reconcileUpdate(ctx, &user)
	// 比对user是否一致，不一致则进行更新
	if !reflect.DeepEqual(originUser.Spec, user.Spec) ||
		!controllerutil.ContainsFinalizer(originUser, userv1alpha1.UserFinalizer) {
		// 2. 更新资源
		if err := r.Update(ctx, &user); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			} else {
				return ctrl.Result{}, err
			}
		} else if len(updateErrs) != 0 {
			klog.Error("Failed to create the following resources: ", updateErrs)
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}
	}

	return r.updateUserStatus(ctx, &user)
}

// SetupWithManager sets up the controller with the Manager.
func (r *UserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	//r.cfg = mgr.GetConfig()
	if clients, err := kubernetes.NewForConfig(mgr.GetConfig()); err != nil {
		return err
	} else {
		r.apiClients = clients
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&userv1alpha1.User{}).
		Complete(r)
}

func (r *UserReconciler) reconcileDelete(ctx context.Context, u *userv1alpha1.User) (ctrl.Result, error) {
	// 1. 删除Finalizer
	controllerutil.RemoveFinalizer(u, userv1alpha1.UserFinalizer)
	userNamespace := v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: u.Spec.Mobilephone,
		},
	}
	// 2. 删除namespace，会自动删除namespace下的资源
	if err := r.Client.Delete(ctx, &userNamespace); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}
	// 3. token和证书自动过期不用删除
	// 删除csr
	if err := r.cleanCSR(ctx, u.GetName()); err != nil {
		return ctrl.Result{}, err
	}

	// 4. 删除所有的Node信息, 需要确定Node的label
	// openyurt/tenant=mobilephone
	listOptions := client.MatchingLabels{"openyurt/tenant": u.Spec.Mobilephone}
	var nodes v1.NodeList
	if err := r.List(context.TODO(), &nodes, listOptions); err != nil {
		return ctrl.Result{}, fmt.Errorf("fail to list the nodes of user %s: %v", u.Name, err)
	}
	for i := range nodes.Items {
		if err := r.Client.Delete(ctx, &nodes.Items[i]); err != nil {
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("fail to delete the node of user %s: %v", nodes.Items[i].Name, err)
			}
		}
	}

	// 5. 删除所有的NodePool
	var nodepools appv1alpha1.NodePoolList
	if err := r.List(context.TODO(), &nodepools, listOptions); err != nil {
		return ctrl.Result{}, fmt.Errorf("fail to list the nodepools of user %s: %v", u.Name, err)
	}
	for i := range nodepools.Items {
		if err := r.Client.Delete(ctx, &nodepools.Items[i]); err != nil {
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("fail to delete the nodepool of user %s: %v", nodepools.Items[i].Name, err)
			}
		}
	}

	// 6. 更新字段
	if err := r.Update(ctx, u); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *UserReconciler) reconcileUpdate(ctx context.Context, user *userv1alpha1.User) map[string]error {
	controllerutil.AddFinalizer(user, userv1alpha1.UserFinalizer)
	errors := map[string]error{}

	if user.Status.Expired == true {
		deleteTime := metav1.Now()
		user.ObjectMeta.DeletionTimestamp = &deleteTime
		return errors
	}

	// 1. 创建临时的集群证书
	if user.Spec.KubeConfig == "" {
		kubeconfig, err := r.generateClusterCredentials(ctx, user)
		if err != nil {
			errors[GenerateKubeConfigErr] = err
		} else {
			if data, err := clientcmd.Write(*kubeconfig); err != nil {
				errors[GenerateKubeConfigErr] = err
			} else {
				user.Spec.KubeConfig = string(data)
			}
		}
	}

	// 2. 创建namespace
	if user.Spec.Namespace == "" {
		userNamespace := v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{"openyurt/tenant":user.Spec.Mobilephone},
				Name: user.Spec.Mobilephone,
			},
		}

		var existsNamespace v1.Namespace
		if err := r.Client.Get(ctx, types.NamespacedName{Name: user.Spec.Mobilephone}, &existsNamespace); err != nil {
			if apierrors.IsNotFound(err) {
				if err := r.Client.Create(ctx, &userNamespace); err != nil {
					errors[GenerateNamespaceErr] = err
				}
			}
		}
		user.Spec.Namespace = user.Spec.Mobilephone
	}

	// 3. 创建接入节点脚本
	if user.Spec.NodeAddScript == "" {
		// 3.1 创建token
		bootstrapToken, err := r.createTempJoinToken(user)
		if err != nil {
			errors[GenerateJoinTokenErr] = err
		}
		kubeConfig, err := GetClusterInfo(r.apiClients)
		if err != nil {
			errors[GenerateJoinTokenErr] = err
		}
		// Get the CA data from the bootstrap client config.
		var server string
		for _, cluster := range kubeConfig.Clusters {
			server = cluster.Server
			klog.Infof("Get Kubeconfig: ", cluster.Server)
		}

		// 3.2 构建openyurt加入节点命令
		joinStr := fmt.Sprintf("wget https://github.com/openyurtio/openyurt/releases/download/v0.6.0/yurtctl; chmod u+x yurtctl; ./yurtctl join %s --token=%s --discovery-token-unsafe-skip-ca-verification --organizations=openyurt:tenant:%s --v=5",
			strings.TrimPrefix(server, "https://"), bootstrapToken, user.Spec.Mobilephone)
		user.Spec.NodeAddScript = joinStr
		user.Spec.Token = bootstrapToken
	}
	if user.Spec.ValidPeriod == 0 {
		user.Spec.ValidPeriod = r.UserValidDays
	}
	return errors
}

func (r *UserReconciler) updateUserStatus(ctx context.Context, user *userv1alpha1.User) (ctrl.Result, error) {
	needUpdate := false
	// 设置创建时间
	if user.Status.EffectiveTime.IsZero() && user.Spec.Namespace != "" && user.Spec.NodeAddScript != "" && user.Spec.KubeConfig != "" {
		user.Status.EffectiveTime = metav1.Now()
		needUpdate = true
	} else {
		return ctrl.Result{Requeue: true}, nil
	}

	userValidityPeriod := time.Duration(24 * user.Spec.ValidPeriod) * time.Hour
	// 判断是否过期
	if !user.Status.EffectiveTime.IsZero() {
		expiredTime := metav1.Time{Time: user.Status.EffectiveTime.Time.Add(userValidityPeriod)}
		nowTime := metav1.Now()
		// 设置过期标志
		if !nowTime.Before(&expiredTime) {
			user.Status.Expired = true
			needUpdate = true
		}
	}

	if needUpdate {
		var newUser userv1alpha1.User

		if err := r.Get(ctx, types.NamespacedName{Namespace: user.Namespace, Name: user.Name}, &newUser); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		newUser.Status = user.Status

		if err := r.Status().Update(ctx, &newUser); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{Requeue: true}, client.IgnoreNotFound(err)
		}
	}
	return ctrl.Result{}, nil
}

// TODO 需要获取token验证是否已经存在相关的token
func (r *UserReconciler) createTempJoinToken(user *userv1alpha1.User) (string, error) {
	// 1. 随机构建 kubeadmapi.BootstrapToken 结构体
	tokenStr, err := bootstraputil.GenerateBootstrapToken()
	if err != nil {
		return "", errors.Wrap(err, "couldn't generate random token")
	}
	bootstrapToken, err := kubeadmapi.NewBootstrapTokenString(tokenStr)
	userValidityPeriod := time.Duration(24 * user.Spec.ValidPeriod) * time.Hour
	token := kubeadmapi.BootstrapToken{
		Token:       bootstrapToken,
		Description: fmt.Sprintf("Short lived bootstrap token used for user: %s", user.Name),
		Groups:      []string{"system:bootstrappers:kubeadm:default-node-token", fmt.Sprintf("system:bootstrappers:openyurt:tenant:%s", user.Spec.Namespace)},
		TTL: &metav1.Duration{
			Duration: userValidityPeriod,
		},
		Usages: []string{"authentication", "signing"},
	}
	// 2. 调用client创建secret
	if err := tokenphase.CreateNewTokens(r.apiClients, []kubeadmapi.BootstrapToken{token}); err != nil {
		return "", err
	}

	// 3. 创建成功后返回join token
	return tokenStr, nil
}

// 生成临时的集群证书
func (r *UserReconciler) generateClusterCredentials(ctx context.Context, user *userv1alpha1.User) (*clientcmdapi.Config, error) {
	// 1. 生成公私钥对，并用公钥生成csr请求
	// * keyPEM就是私钥privateKey的字节版
	_, csrPEM, keyPEM, privateKey, err := r.generateCSR(user)
	signerName := certificatesv1.KubeAPIServerClientSignerName
	usages := []certificatesv1.KeyUsage{
		certificatesv1.UsageDigitalSignature,
		certificatesv1.UsageKeyEncipherment,
		certificatesv1.UsageClientAuth,
	}
	// 2. 创建CSR请求
	csrName := user.Name
	// 检查是否存在，存在则删除
	err = r.cleanCSR(ctx, csrName)
	if err != nil {
		return nil, fmt.Errorf("failed to clean the old CSR: %v", err)
	}

	//requestedDuration := 600 * time.Second
	reqN, reqId, err := csr.RequestCertificate(r.apiClients, csrPEM, csrName, signerName,usages, privateKey)
	//req, err := csr.RequestCertificate(clientSet.CertificatesV1beta1().CertificateSigningRequests(), csrPEM, csrName, signerName, usages, privateKey)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("failed while requesting a signed certificate from the master: %v", err))
		return nil, err
	}
	// 3. 发送并等待csr回复
	// * crtPEM就是签名后的数字证书
	//crtPEM, err := csr.WaitForCertificate(ctx, clientSet.CertificatesV1beta1().CertificateSigningRequests(), req)
	crtPEM, err := csr.WaitForCertificate(ctx, r.apiClients, reqN, reqId)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("certificate request was not signed: %v", err))
		return nil, err
	}

	//4. 生成kubeConfig
	return CreateKubeConfigFile(r.apiClients, user, crtPEM, keyPEM), nil
}

func (r *UserReconciler) cleanCSR(ctx context.Context, reqName string) error {
	if err := r.apiClients.CertificatesV1beta1().CertificateSigningRequests().Delete(ctx, reqName, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *UserReconciler) generateCSR(user *userv1alpha1.User) (template *x509.CertificateRequest, csrPEM []byte, keyPEM []byte, key interface{}, err error) {
	// Generate a new private key. privateKey里面就包含了公私钥
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("unable to generate a new private key: %v", err)
	}
	der, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("unable to marshal the new key to DER: %v", err)
	}

	keyPEM = pem.EncodeToMemory(&pem.Block{Type: keyutil.ECPrivateKeyBlockType, Bytes: der})

	template = &x509.CertificateRequest{
		// 表示使用这个数字证书的使用者(组织和用户信息)
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("openyurt:tenant:%s", user.Spec.Mobilephone),
			Organization: []string{"openyurt:users"},
		},
	}
	csrPEM, err = cert.MakeCSRFromTemplate(privateKey, template)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("unable to create a csr from the private key: %v", err)
	}
	return template, csrPEM, keyPEM, privateKey, nil
}

// CreateKubeConfigFile 根据传入的参数生成kubeconfig并保存到本地中
// kubeClientConfig是当前客户端与k8s进行通信的文件，主要获取apiserver的CA文件和地址
func CreateKubeConfigFile(cs clientset.Interface, user *userv1alpha1.User, certPEM []byte, keyPEM []byte) *clientcmdapi.Config {
	kubeConfig, err := GetClusterInfo(cs)
	if err != nil {
		klog.Errorf("Get Kubeconfig error", err)
		return nil
	}
	// Get the CA data from the bootstrap client config.
	var clusterCABytes []byte
	var server string
	for _, cluster := range kubeConfig.Clusters {
		clusterCABytes = cluster.CertificateAuthorityData
		server = cluster.Server
		klog.Infof("Get Kubeconfig: ", cluster.Server)
	}

	// Build resulting kubeconfig.
	kubeconfigData := clientcmdapi.Config{
		// Define a cluster stanza based on the bootstrap kubeconfig.
		Clusters: map[string]*clientcmdapi.Cluster{
			"default-cluster": {
				// 需要获取
				Server: server,
				//// 需要获取
				//InsecureSkipTLSVerify: kubeClientConfig.Insecure,
				// 需要获取
				CertificateAuthorityData: clusterCABytes,
			},
		},
		// Define auth based on the obtained client cert.
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			// 这个name是否需要指定为租户的名称
			fmt.Sprintf("user-%s", user.Spec.Mobilephone): {
				ClientCertificateData: certPEM,
				ClientKeyData:         keyPEM,
			},
		},
		// Define a context that connects the auth info and cluster, and set it as the default
		Contexts: map[string]*clientcmdapi.Context{
			"default-context": {
				Cluster:   "default-cluster",
				AuthInfo:  fmt.Sprintf("user-%s", user.Spec.Mobilephone),
				Namespace: "default",
			},
		},
		CurrentContext: "default-context",
	}

	return &kubeconfigData
}

func GetClusterInfo(cs clientset.Interface) (*clientcmdapi.Config, error) {
	//insecureClient, err := clientset.NewForConfig(kubeClientConfig)
	//if err != nil {
	//	klog.Errorf("could not new insecure client, %v", err)
	//	return nil, err
	//}

	// make sure configMap kube-public/cluster-info in k8s cluster beforehand
	// insecureClusterInfo, err := insecureClient.CoreV1().ConfigMaps(metav1.NamespacePublic).Get(context.Background(), "cluster-info", metav1.GetOptions{})
	insecureClusterInfo, err := cs.CoreV1().ConfigMaps(metav1.NamespacePublic).Get(context.Background(), "cluster-info", metav1.GetOptions{})
	if err != nil {
		klog.Errorf("failed to get cluster-info configmap, %v", err)
		return nil, err
	}

	kubeconfigStr, ok := insecureClusterInfo.Data["kubeconfig"]
	if !ok || len(kubeconfigStr) == 0 {
		return nil, fmt.Errorf("no kubeconfig in cluster-info configmap of kube-public namespace")
	}

	kubeConfig, err := clientcmd.Load([]byte(kubeconfigStr))
	if err != nil {
		return nil, fmt.Errorf("could not load kube config string, %v", err)
	}

	if len(kubeConfig.Clusters) != 1 {
		return nil, fmt.Errorf("more than one cluster setting in cluster-info configmap")
	}

	return kubeConfig, nil
}
