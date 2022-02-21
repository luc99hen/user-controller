/*
Copyright 2020 The OpenYurt Authors.

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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmgr "sigs.k8s.io/controller-runtime/pkg/manager"

	userv1alpha1 "github.com/openyurtio/yurt-user-controller/api/v1alpha1"
)

const DefaultUserSyncPeriod = 120

type UserSyncer struct {
	// syncing period in seconds
	syncPeriod time.Duration
	client.Client
}

func NewUserSyncer(client client.Client, periodMinutes int) (UserSyncer, error) {
	if periodMinutes <= 0 {
		periodMinutes = DefaultUserSyncPeriod
	}
	return UserSyncer{
		syncPeriod: time.Duration(periodMinutes) * time.Minute,
		Client: client,
	}, nil
}

func (us *UserSyncer) NewUserSyncerRunnable() ctrlmgr.RunnableFunc {
	return func(ctx context.Context) error {
		us.Run(ctx.Done())
		return nil
	}
}

func (us *UserSyncer) Run(stop <-chan struct{}) {
	klog.Info("Starting the UserSyncer...")
	go func() {
		for {
			<- time.After(us.syncPeriod)
			var users userv1alpha1.UserList
			if err := us.List(context.TODO(), &users); err != nil{
				klog.Error(err, "fail to list the users")
				continue
			}
			// get expired users
			expiredUsers := make([]userv1alpha1.User, 0)
			for i, user := range users.Items {
				if !user.Status.EffectiveTime.IsZero() {
					userValidityPeriod := time.Duration(24 * user.Spec.ValidPeriod) * time.Hour
					expiredTime := metav1.Time{Time: user.Status.EffectiveTime.Time.Add(userValidityPeriod)}
					nowTime := metav1.Now()
					if !nowTime.Before(&expiredTime) {
						expiredUsers = append(expiredUsers, users.Items[i])
					}
				}
			}
			// delete expired users and record the re
			deleteFailedUsers := make([]string, 0)
			deleteSucceedUsers := make([]string, 0)
			for i := range expiredUsers {
				if err := us.Client.Delete(context.TODO(), &expiredUsers[i]); err != nil {
					deleteFailedUsers = append(deleteFailedUsers, expiredUsers[i].Name)
				} else {
					deleteSucceedUsers = append(deleteSucceedUsers, expiredUsers[i].Name)
				}
			}
			klog.V(2).Info("One round of user cleanup has been completed")
			if len(deleteFailedUsers) != 0 {
				klog.Errorf("Failed to delete the following expired users: %v", deleteFailedUsers)
			} else if len(deleteSucceedUsers) != 0{
				klog.Errorf("Succeed to delete the following expired users: %v", deleteSucceedUsers)
			}
		}
	}()

	<-stop
	klog.Info("Stopping the UserSyncer...")
}
