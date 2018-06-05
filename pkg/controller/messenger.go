package controller

import (
	"fmt"
	"strings"

	"github.com/appscode/envconfig"
	"github.com/appscode/go-notify"
	"github.com/appscode/go-notify/unified"
	"github.com/appscode/kubernetes-webhook-util/admission"
	hooks "github.com/appscode/kubernetes-webhook-util/admission/v1beta1"
	webhook "github.com/appscode/kubernetes-webhook-util/admission/v1beta1/generic"
	"github.com/appscode/kutil/tools/queue"
	"github.com/golang/glog"
	"github.com/kubeware/messenger/apis/messenger"
	api "github.com/kubeware/messenger/apis/messenger/v1alpha1"
	"github.com/tamalsaha/go-oneliners"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"github.com/appscode/kutil/meta"
	"time"
	"github.com/kubeware/messenger/client/clientset/versioned/typed/messenger/v1alpha1/util"
)

func (c *MessengerController) NewNotifierWebhook() hooks.AdmissionHook {
	return webhook.NewGenericWebhook(
		schema.GroupVersionResource{
			Group:    "admission.messenger.kubeware.io",
			Version:  "v1alpha1",
			Resource: api.ResourceMessagingServices,
		},
		api.ResourceMessagingService,
		[]string{messenger.GroupName},
		api.SchemeGroupVersion.WithKind(api.ResourceKindMessagingService),
		nil,
		&admission.ResourceHandlerFuncs{
			CreateFunc: func(obj runtime.Object) (runtime.Object, error) {
				return nil, obj.(*api.MessagingService).IsValid()
			},
			UpdateFunc: func(oldObj, newObj runtime.Object) (runtime.Object, error) {
				return nil, newObj.(*api.MessagingService).IsValid()
			},
		},
	)
}
func (c *MessengerController) initMessageWatcher() {
	c.messageInformer = c.messengerInformerFactory.Messenger().V1alpha1().Messages().Informer()
	c.messageQueue = queue.New(api.ResourceKindMessage, c.MaxNumRequeues, c.NumThreads, c.reconcileMessage)
	c.messageInformer.AddEventHandler(queue.DefaultEventHandler(c.messageQueue.GetQueue()))
	c.messageLister = c.messengerInformerFactory.Messenger().V1alpha1().Messages().Lister()
}

func (c *MessengerController) reconcileMessage(key string) error {
	obj, exist, err := c.messageInformer.GetIndexer().GetByKey(key)
	if err != nil {
		glog.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}

	if !exist {
		glog.Warningf("Notifier %s does not exist anymore\n", key)
	} else {
		glog.Infof("Sync/Add/Update for Notifier %s\n", key)

		msg := obj.(*api.Message)
		fmt.Println(">>>>>>>>>>>> Message crd obj name", msg.Name)
		oneliners.PrettyJson(*msg, "MessageCrdObj")
		msgStatus := &api.MessageStatus{}
		err := c.send(msg)
		if err != nil {
			msgStatus.ErrorMessage = fmt.Sprintf("Sending Message with key %s failed: %v", key, err)
			glog.Errorf(msgStatus.ErrorMessage)
		} else {
			msgStatus.SentTimestamp = &metav1.Time{time.Now()}
			glog.Infof("Message with key %s has been sent", key)
		}

		_, updateErr := util.UpdateMessageStatus(c.messengerClient.MessengerV1alpha1(), msg, func(status *api.MessageStatus) *api.MessageStatus {
			return msgStatus
		})
		if updateErr != nil {
			glog.Errorf("Failed to update status for Message with key %s: %v", key, updateErr)
			return err
		}
	}
	return nil
}

func (c *MessengerController) deleteMessengerNotifier(repository *api.Message) error {
	return nil
}

func (c *MessengerController) send(msg *api.Message) error {
	fmt.Println(">>>>>>>>>>>>> Send().......")
	messagingService, err := c.messengerClient.MessengerV1alpha1().MessagingServices(msg.Namespace).Get(msg.Spec.Service, metav1.GetOptions{})
	if err != nil {
		fmt.Println(">>>>>>>", )
		return err
	}

	oneliners.PrettyJson(*messagingService, "messagingService")

	cred, err := c.getLoader(messagingService.Spec.CredentialSecretName)
	if err != nil {
		return err
	}

	notifier, err := unified.LoadVia(strings.ToLower(messagingService.Spec.Drive), cred)
	if err != nil {
		return err
	}

	switch n := notifier.(type) {
	case notify.ByEmail:
		return n.To(messagingService.Spec.To[0], messagingService.Spec.To[1:]...).
			WithSubject(msg.Spec.Email).
			WithBody(msg.Spec.Message).
			WithNoTracking().
			Send()
	case notify.BySMS:
		return n.To(messagingService.Spec.To[0], messagingService.Spec.To[1:]...).
			WithBody(msg.Spec.Email).
			Send()
	case notify.ByChat:
		return n.To(messagingService.Spec.To[0], messagingService.Spec.To[1:]...).
			WithBody(msg.Spec.Chat).
			Send()
	case notify.ByPush:
		return n.To(messagingService.Spec.To...).
			WithBody(msg.Spec.Chat).
			Send()
	}

	return nil
}

func (c *MessengerController) getLoader(credentialSecretName string) (envconfig.LoaderFunc, error) {
	if credentialSecretName == "" {
		return func(key string) (string, bool) {
			return "", false
		}, nil
	}
	cfg, err := c.kubeClient.CoreV1().
		Secrets(meta.Namespace()).
		Get(credentialSecretName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return func(key string) (value string, found bool) {
		var bytes []byte
		bytes, found = cfg.Data[key]
		value = string(bytes)
		return
	}, nil
}