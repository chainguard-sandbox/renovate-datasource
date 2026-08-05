// Package chart defines Helm chart artifact types. Fetching lives in
// package oci, diffing in package diff.
package chart

// HelmShImagesAnnotation is the OCI manifest annotation Chainguard's
// iamguarded charts use to list their referenced images.
const HelmShImagesAnnotation = "helm.sh/images"

// Contents is the parsed content of a chart OCI artifact.
type Contents struct {
	ChartYAML    []byte
	ChartVersion string
	AppVersion   string
	ValuesYAML   []byte
	// AnnotationImages is the image list parsed from the
	// helm.sh/images OCI manifest annotation, nil when absent.
	AnnotationImages []LockedImage
}
