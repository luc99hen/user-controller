package controllers

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	certificates "k8s.io/api/certificates/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	certv1beta1 "k8s.io/client-go/informers/certificates/v1beta1"
	"k8s.io/client-go/kubernetes"
	typev1beta1 "k8s.io/client-go/kubernetes/typed/certificates/v1beta1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmgr "sigs.k8s.io/controller-runtime/pkg/manager"
)

type UserCSRApprover struct {
	client.Client
	csrInformer certv1beta1.CertificateSigningRequestInformer
	csrClient   typev1beta1.CertificateSigningRequestInterface
	period      time.Duration
}

func NewUserCSRApprover(client client.Client, clientset kubernetes.Interface, periodSecs time.Duration) (UserCSRApprover, error) {
	return UserCSRApprover{
		Client:    client,
		csrClient: clientset.CertificatesV1beta1().CertificateSigningRequests(),
		period:    periodSecs,
	}, nil
}

func (uca *UserCSRApprover) NewUserCSRApproverRunnable() ctrlmgr.RunnableFunc {
	return func(ctx context.Context) error {
		uca.Run(ctx.Done())
		return nil
	}
}

func (uca *UserCSRApprover) Run(stop <-chan struct{}) {
	klog.V(1).Info("starting the UserCSRApprover...")
	go func() {
		for {
			<-time.After(uca.period)
			var csrList *certificates.CertificateSigningRequestList
			// 获取所有的csr请求
			csrList, _ = uca.csrClient.List(context.TODO(), metav1.ListOptions{})
			// 检查csr状态, 并approve csr
			for i := range csrList.Items {
				csr := csrList.Items[i]
				if uca.isUserCSR(&csr) || uca.isNodeCSR(&csr) {
					if err := approveUserCSR(&csr, uca.csrClient); err != nil {
						klog.Errorf("failed to approve csr(%s), %v", csr.GetName(), err)
					}
				}
			}
			//klog.Info("check csr once")
			// 设置csr的过期时间, 设置spec.expirationSeconds这个功能只有1.22版本之后才有，之前都是没有的
		}
	}()

	<-stop
	klog.V(1).Info("stopping the deviceService syncer")
}

func (uca *UserCSRApprover) isUserCSR(csr *certificates.CertificateSigningRequest) bool {
	pemBytes := csr.Spec.Request
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return false
	}
	x509cr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return false
	}
	for i, org := range x509cr.Subject.Organization {
		if org == "openyurt:users" {
			break
		}
		if i == len(x509cr.Subject.Organization)-1 {
			return false
		}
	}
	return true
}

func (uca *UserCSRApprover) isNodeCSR(csr *certificates.CertificateSigningRequest) bool {
	pemBytes := csr.Spec.Request
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return false
	}
	x509cr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return false
	}
	klog.V(2).Infof("CSR Org: %s", x509cr.Subject.Organization)
	if len(x509cr.Subject.Organization) == 2 {
		if x509cr.Subject.Organization[0] == "system:nodes" && strings.Contains(x509cr.Subject.Organization[1], "openyurt:tenant:") {
			return true
		} else if x509cr.Subject.Organization[1] == "system:nodes" && strings.Contains(x509cr.Subject.Organization[0], "openyurt:tenant:") {
			return true
		}
	}
	return false
}

// approveUserCSR checks the csr status, if it is neither approved nor
// denied, it will try to approve the csr.
func approveUserCSR(csr *certificates.CertificateSigningRequest, csrClient typev1beta1.CertificateSigningRequestInterface) error {
	approved, denied := checkCertApprovalCondition(&csr.Status)
	if approved {
		klog.V(4).Info("csr(%s) is approved", csr.GetName())
		return nil
	}

	if denied {
		klog.Infof("csr(%s) is denied", csr.GetName())
		return nil
	}

	// approve the yurthub related csr
	csr.Status.Conditions = append(csr.Status.Conditions,
		certificates.CertificateSigningRequestCondition{
			Type:    certificates.CertificateApproved,
			Reason:  "AutoApproved",
			Message: fmt.Sprintf("self-approving user csr"),
		})
	//klog.Info("successfully approve csr, %s", csr)
	result, err := csrClient.UpdateApproval(context.Background(), csr, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("failed to approve csr(%s), %v", csr.GetName(), err)
		return err
	}
	klog.Infof("successfully approve csr: %s", result.Name)
	return nil
}

// checkCertApprovalCondition checks if the given csr's status is
// approved or denied
func checkCertApprovalCondition(status *certificates.CertificateSigningRequestStatus) (approved bool, denied bool) {
	for _, c := range status.Conditions {
		if c.Type == certificates.CertificateApproved {
			approved = true
		}
		if c.Type == certificates.CertificateDenied {
			denied = true
		}
	}
	return
}
