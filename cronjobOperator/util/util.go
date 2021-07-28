package util

import v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

func HasOwnerReference(list []v1.OwnerReference, searching v1.OwnerReference) bool {
	for _, or := range list {
		if or.Kind == searching.Kind && or.Name == searching.Name {
			return true
		}
	}

	return false
}
