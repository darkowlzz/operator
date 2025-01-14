package storageoscluster

import (
	"context"
	"fmt"
	"os"

	"github.com/darkowlzz/operator-toolkit/declarative"
	"github.com/darkowlzz/operator-toolkit/declarative/kustomize"
	"github.com/darkowlzz/operator-toolkit/declarative/transform"
	eventv1 "github.com/darkowlzz/operator-toolkit/event/v1"
	"github.com/darkowlzz/operator-toolkit/operator/v1/operand"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/kustomize/api/filesys"
	kustomizetypes "sigs.k8s.io/kustomize/api/types"

	storageoscomv1 "github.com/storageos/operator/api/v1"
	"github.com/storageos/operator/internal/image"
	stransform "github.com/storageos/operator/internal/transform"
)

const (
	// apiManagerPackage contains the resource manifests for api-manager
	// operand.
	apiManagerPackage = "api-manager"

	// Kustomize image name for container image.
	kImageAPIManager = "api-manager"

	// Related image environment variable.
	apiManagerImageEnvVar = "RELATED_IMAGE_API_MANAGER"
)

type APIManagerOperand struct {
	name            string
	client          client.Client
	requires        []string
	requeueStrategy operand.RequeueStrategy
	fs              filesys.FileSystem
}

var _ operand.Operand = &APIManagerOperand{}

func (c *APIManagerOperand) Name() string                             { return c.name }
func (c *APIManagerOperand) Requires() []string                       { return c.requires }
func (c *APIManagerOperand) RequeueStrategy() operand.RequeueStrategy { return c.requeueStrategy }
func (c *APIManagerOperand) ReadyCheck(ctx context.Context, obj client.Object) (bool, error) {
	return true, nil
}

func (c *APIManagerOperand) Ensure(ctx context.Context, obj client.Object, ownerRef metav1.OwnerReference) (eventv1.ReconcilerEvent, error) {
	ctx, span, _, _ := instrumentation.Start(ctx, "APIManagerOperand.Ensure")
	defer span.End()

	b, err := getAPIManagerBuilder(c.fs, obj)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	return nil, b.Apply(ctx)
}

func (c *APIManagerOperand) Delete(ctx context.Context, obj client.Object) (eventv1.ReconcilerEvent, error) {
	ctx, span, _, _ := instrumentation.Start(ctx, "APIManagerOperand.Delete")
	defer span.End()

	b, err := getAPIManagerBuilder(c.fs, obj)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	return nil, b.Delete(ctx)
}

func getAPIManagerBuilder(fs filesys.FileSystem, obj client.Object) (*declarative.Builder, error) {
	cluster, ok := obj.(*storageoscomv1.StorageOSCluster)
	if !ok {
		return nil, fmt.Errorf("failed to convert %v to StorageOSCluster", obj)
	}

	// Get image name.
	images := []kustomizetypes.Image{}

	// Check environment variables for related images.
	relatedImages := image.NamedImages{
		kImageAPIManager: os.Getenv(apiManagerImageEnvVar),
	}
	images = append(images, image.GetKustomizeImageList(relatedImages)...)

	// Get the images from the cluster spec. This overwrites the default image
	// set by the operator related images environment variables.
	namedImages := image.NamedImages{
		kImageAPIManager: cluster.Spec.Images.APIManagerContainer,
	}
	images = append(images, image.GetKustomizeImageList(namedImages)...)

	// Create deployment transforms.
	deploymentTransforms := []transform.TransformFunc{}

	// Add secret volume transform.
	apiSecretVolTF := stransform.SetPodTemplateSecretVolumeFunc("api-secret", cluster.Spec.SecretRefName, nil)

	deploymentTransforms = append(deploymentTransforms, apiSecretVolTF)

	roleBindingTransforms := []transform.TransformFunc{}

	// Add namespace of cross-referenced role binding subject.
	// NOTE: In kustomize, when using role binding, if the subject service
	// account is not defined in the same kustomization file, kustomize doesn't
	// update the subject namespace of the service account. Set the cross
	// referenced service account namespace.
	// Refer: https://github.com/kubernetes-sigs/kustomize/issues/1377
	daemonsetSASubjectNamespaceTF := stransform.SetClusterRoleBindingSubjectNamespaceFunc("storageos-daemonset-sa", cluster.GetNamespace())

	roleBindingTransforms = append(roleBindingTransforms, daemonsetSASubjectNamespaceTF)

	return declarative.NewBuilder(apiManagerPackage, fs,
		declarative.WithManifestTransform(transform.ManifestTransform{
			"api-manager/deployment.yaml":                  deploymentTransforms,
			"api-manager/key-management-role-binding.yaml": roleBindingTransforms,
		}),
		declarative.WithKustomizeMutationFunc([]kustomize.MutateFunc{
			kustomize.AddNamespace(cluster.GetNamespace()),
			kustomize.AddImages(images),
		}),
	)
}

func NewAPIManagerOperand(
	name string,
	client client.Client,
	requires []string,
	requeueStrategy operand.RequeueStrategy,
	fs filesys.FileSystem,
) *APIManagerOperand {
	return &APIManagerOperand{
		name:            name,
		client:          client,
		requires:        requires,
		requeueStrategy: requeueStrategy,
		fs:              fs,
	}
}
