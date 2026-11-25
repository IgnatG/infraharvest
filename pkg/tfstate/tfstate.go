// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package tfstate reads Terraform and OpenTofu state, from files and from
// the places the backends keep it (Amazon S3, Google Cloud Storage, Azure
// Blob Storage and HCP Terraform or Terraform Enterprise), without running
// Terraform: no init, no plan, no binary, only the credentials of the
// store. It is the reader infraharvest finds the resources a root already
// manages with, offered so that other tools read state the same way.
//
// Read returns a Document for each state a location names: its serial,
// lineage and Terraform version, each resource instance with its address,
// provider, ID, ARN and the addresses it depends on, the names of its
// outputs, and the places its terraform_remote_state data sources read
// other states from. State holds secrets, so the reader keeps nothing
// else: the values of attributes and of outputs are dropped while the
// state is decoded, and only the ID and ARN of each instance, and the
// location a remote state is read from (never its credentials), are kept.
// A state is decoded as it is read, one resource at a time, so that a
// large one doesn't take its size in memory.
//
// The stores are packages of their own under stores/, each linking only
// its own cloud's SDK: a program passes the Stores it wants, and a build
// links the clouds it names, no more.
//
// # Promises
//
// State format version 4, as Terraform 0.12 and later and OpenTofu write
// it, is read; version 3 is refused with a hint to upgrade it. The fields
// of Document, Instance, Output and RemoteState, and the forms of
// location Read takes, only grow. Until infraharvest's first tagged
// release, everything in pkg/ is unstable.
//
// # Licence
//
// Like the rest of infraharvest it is AGPL-3.0-only. A product that is not
// licensed under the AGPL needs infraharvest's commercial licence to
// import it (see the licensing section of the README).
package tfstate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// ObjectStore reads state from a bucket.
type ObjectStore interface {
	// List returns the keys under prefix.
	List(ctx context.Context, bucket, prefix string) ([]string, error)
	// Get returns the content of the object at key, which is read whole
	// (see ReadState), and nil, with no error, for an object that has no
	// state.
	Get(ctx context.Context, bucket, key string) ([]byte, error)
}

// StreamStore is an ObjectStore that can also open an object to read as
// it goes: its states are decoded as they are read (see ParseFrom),
// rather than read whole first. Open returns nil, and no error, for an
// object that has no state, as Get does.
type StreamStore interface {
	ObjectStore
	Open(ctx context.Context, bucket, key string) (io.ReadCloser, error)
}

// DefaultHCPTerraformHost is the host of HCP Terraform (formerly Terraform
// Cloud); Terraform Enterprise has its own.
const DefaultHCPTerraformHost = "app.terraform.io"

// Stores open the object stores state is read from: S3 in a region, with
// the credentials of a shared config profile ("" for the defaults), Cloud
// Storage, the Azure Blob Storage account at a service URL, and the
// workspaces of HCP Terraform or Terraform Enterprise at a host. Each
// store is a package of its own under stores/, which links its cloud's
// SDK: set the fields of the clouds you read, with the New of the store's
// package, and no other SDK is linked. A field left nil reads no state of
// that kind, and Read says so rather than failing obscurely.
type Stores struct {
	S3           func(ctx context.Context, region, profile string) (ObjectStore, error)
	GCS          func(ctx context.Context) (ObjectStore, error)
	AzureBlob    func(ctx context.Context, serviceURL string) (ObjectStore, error)
	HCPTerraform func(ctx context.Context, host string) (ObjectStore, error)
}

// errNoStore is the error of a source whose kind of store, such as S3, the
// Stores given have no way to open: a caller that leaves one out reads
// no state of that kind, and says so rather than panicking.
func errNoStore(kind string) error {
	return fmt.Errorf("no %s store to read the state with", kind)
}

// stateSizeLimit is the most bytes one state may take: a store may read
// state whole into memory (see ReadState), and an object that big under a
// prefix is most likely not state.
var stateSizeLimit int64 = 512 << 20

// ReadState reads one state from r, failing rather than cutting it off if
// it takes more than 512 MiB. Every ObjectStore reads what it gets
// through it, so that each source has the same limit.
func ReadState(r io.Reader) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(r, stateSizeLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > stateSizeLimit {
		return nil, fmt.Errorf("state is larger than %d MiB, the most infraharvest reads", stateSizeLimit>>20)
	}
	return content, nil
}

// WithoutURL returns the error a *url.Error wraps, whose message quotes
// the URL, or err. A store whose URLs carry a credential, such as a SAS
// token or a signed download URL, returns its errors through it, so that
// no error, log or MCP client sees one.
func WithoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// The hosts of Blob Storage in Azure's clouds: public, US Government and
// China.
const (
	publicBlobSuffix     = "blob.core.windows.net"
	governmentBlobSuffix = "blob.core.usgovcloudapi.net"
	chinaBlobSuffix      = "blob.core.chinacloudapi.cn"
)

// azureBlobSuffixes are the hosts of Blob Storage in Azure's clouds. The
// credentials the azurerm backend uses go to the host a source names, so
// no other host is read from.
var azureBlobSuffixes = []string{publicBlobSuffix, governmentBlobSuffix, chinaBlobSuffix}

// AzureBlobSuffix returns the host of Blob Storage in the Azure cloud
// environment names, as ARM_ENVIRONMENT does to the azurerm backend when
// its configuration names none. It takes the names the Azure provider
// command takes, such as usgovernment or AzureChinaCloud, by dropping a
// leading "azure" and a trailing "cloud" first; "" is the public cloud.
func AzureBlobSuffix(environment string) (string, error) {
	switch strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(environment)), "azure"), "cloud") {
	case "", "public", "global":
		return publicBlobSuffix, nil
	case "usgovernment", "usgovernmentl4", "usgovernmentl5", "dod":
		return governmentBlobSuffix, nil
	case "china":
		return chinaBlobSuffix, nil
	}
	return "", fmt.Errorf("ARM_ENVIRONMENT %q: state is read from Blob Storage in Azure's public, US Government and China clouds only", environment)
}
