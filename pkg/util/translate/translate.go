package translate

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var (
	MarkerLabel = "vcluster.loft.sh/managed-by"
	Suffix      = "suffix"
)

func Split(s, sep string) (string, string) {
	parts := strings.SplitN(s, sep, 2)
	return strings.TrimSpace(parts[0]), strings.TrimSpace(safeIndex(parts, 1))
}

func safeIndex(parts []string, idx int) string {
	if len(parts) <= idx {
		return ""
	}
	return parts[idx]
}

func SafeConcatName(name ...string) string {
	fullPath := strings.Join(name, "-")
	if len(fullPath) > 63 {
		digest := sha256.Sum256([]byte(fullPath))
		return fullPath[0:52] + "-" + hex.EncodeToString(digest[0:])[0:10]
	}
	return fullPath
}

func SetExcept(from map[string]string, to map[string]string, except ...string) map[string]string {
	retMap := map[string]string{}
	if from != nil {
		for k, v := range from {
			if exists(except, k) {
				continue
			}

			retMap[k] = v
		}
	}

	if to != nil {
		for _, k := range except {
			if to[k] != "" {
				retMap[k] = to[k]
			}
		}
	}

	if len(retMap) == 0 {
		return nil
	}

	return retMap
}

func UniqueSlice(stringSlice []string) []string {
	keys := make(map[string]bool)
	list := []string{}
	for _, entry := range stringSlice {
		if entry == "" {
			continue
		}
		if _, value := keys[entry]; !value {
			keys[entry] = true
			list = append(list, entry)
		}
	}
	return list
}

func EqualExcept(a map[string]string, b map[string]string, except ...string) bool {
	for k, v := range a {
		if exists(except, k) {
			continue
		}

		if b == nil || b[k] != v {
			return false
		}
	}

	for k, v := range b {
		if exists(except, k) {
			continue
		}

		if a == nil || a[k] != v {
			return false
		}
	}

	return true
}

func exists(a []string, k string) bool {
	for _, i := range a {
		if i == k {
			return true
		}
	}

	return false
}

func IsManaged(obj runtime.Object) bool {
	meta, err := meta.Accessor(obj)
	if err != nil {
		return false
	} else if meta.GetLabels() == nil {
		return false
	}

	return meta.GetLabels()[MarkerLabel] == Suffix
}

// Returns the physical name of the name / namespace resource
func PhysicalName(name, namespace string) string {
	return SafeConcatName(name, "x", namespace, "x", Suffix)
}

// Returns the translated physical name of this object
func ObjectPhysicalName(obj runtime.Object) string {
	metaAccessor, err := meta.Accessor(obj)
	if err != nil {
		return ""
	}

	return PhysicalName(metaAccessor.GetName(), metaAccessor.GetNamespace())
}

func SetupMetadata(targetNamespace string, obj runtime.Object) (runtime.Object, error) {
	target := obj.DeepCopyObject()
	if err := initMetadata(targetNamespace, target); err != nil {
		return nil, err
	}

	return target, nil
}

// ResetObjectMetadata resets the objects metadata except name, namespace and annotations
func ResetObjectMetadata(obj metav1.Object) {
	obj.SetGenerateName("")
	obj.SetSelfLink("")
	obj.SetUID("")
	obj.SetResourceVersion("")
	obj.SetGeneration(0)
	obj.SetCreationTimestamp(metav1.Time{})
	obj.SetDeletionTimestamp(nil)
	obj.SetDeletionGracePeriodSeconds(nil)
	obj.SetOwnerReferences(nil)
	obj.SetFinalizers(nil)
	obj.SetClusterName("")
	obj.SetManagedFields(nil)
	obj.SetLabels(nil)
}

var OwningStatefulSet *appsv1.StatefulSet

func initMetadata(targetNamespace string, target runtime.Object) error {
	m, err := meta.Accessor(target)
	if err != nil {
		return err
	}

	// reset metadata & translate name and namespace
	name, namespace := m.GetName(), m.GetNamespace()
	ResetObjectMetadata(m)
	m.SetName(PhysicalName(name, namespace))
	m.SetNamespace(targetNamespace)

	// make sure the annotations are set
	labels := m.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[MarkerLabel] = Suffix
	m.SetLabels(labels)

	// set owning stateful set if defined
	if OwningStatefulSet != nil {
		m.SetOwnerReferences([]metav1.OwnerReference{
			{
				APIVersion: appsv1.SchemeGroupVersion.String(),
				Kind:       "StatefulSet",
				Name:       OwningStatefulSet.Name,
				UID:        OwningStatefulSet.UID,
			},
		})
	}

	return nil
}
