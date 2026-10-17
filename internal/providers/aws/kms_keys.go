// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"cmp"
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// KmsLister lists the region's customer managed KMS keys, their grants
// and their aliases (the `kms` service).
type KmsLister struct {
	Service
}

// List records the keys, each followed by its grants, then the
// aliases. Only keys a customer manages and that aren't being deleted are
// listed, and only the aliases of such keys: AWS manages the rest, and a key
// on its way out can't be imported for long. The aliases are listed first,
// as a key is named after its alias. Failing to list keys or aliases fails
// the service; a key or an alias whose key can't be described, and a key
// whose grants can't be listed, is left out, and the failures are returned
// together as a partial error (see partFailures).
func (g *KmsLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	lister := &kmsLister{g: g, svc: kms.NewFromConfig(config), keys: map[string]*types.KeyMetadata{}}
	aliases, err := lister.listAliases()
	if err != nil {
		return errors.Join(err, lister.parts.err())
	}
	if err := lister.addKeys(keyNames(aliases)); err != nil {
		return errors.Join(err, lister.parts.err())
	}
	for _, alias := range aliases {
		name := aws.ToString(alias.AliasName)
		g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_kms_alias", "aws"))
	}
	return lister.parts.err()
}

// keyNames names each key that has aliases after the first of them in
// alphabetical order, without its alias/ prefix, by key ID.
func keyNames(aliases []types.AliasListEntry) map[string]string {
	names := map[string]string{}
	for _, alias := range aliases {
		name := strings.TrimPrefix(aws.ToString(alias.AliasName), "alias/")
		if key := aws.ToString(alias.TargetKeyId); names[key] == "" || name < names[key] {
			names[key] = name
		}
	}
	return names
}

// kmsLister holds what one listing of the KMS service shares: the client,
// and the keys described so far, so that an alias of a key listed earlier
// doesn't describe that key again; and the parts of the listing that
// failed.
type kmsLister struct {
	g     *KmsLister
	svc   *kms.Client
	keys  map[string]*types.KeyMetadata
	parts partFailures
}

// addKeys records every listable key, imported by its ID, which it also
// records as the key_id attribute, followed by its grants. The key's type
// comes from the metadata already described (see kmsKeyType).
// A key is named after its alias, in names by key ID, or else its ID. The
// keys of a page are described, and their grants listed, resourceParallel
// at a time, and recorded in the page's order.
func (l *kmsLister) addKeys(names map[string]string) error {
	pages := kms.NewListKeysPaginator(l.svc, &kms.ListKeysInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(l.g.Context())
		if err != nil {
			return err
		}
		ids := make([]string, len(page.Keys))
		for i, key := range page.Keys {
			ids[i] = aws.ToString(key.KeyId)
		}
		l.describeAll(ids)
		var listable []string
		for _, id := range ids {
			if kmsKeyListable(l.keys[id]) {
				listable = append(listable, id)
			}
		}
		grants := make([][]harvest.Resource, len(listable))
		grantErrs := make([]error, len(listable))
		inParallel(len(listable), resourceParallel, func(i int) {
			id := listable[i]
			grants[i], grantErrs[i] = l.grants(id, cmp.Or(names[id], id))
		})
		for i, id := range listable {
			name := cmp.Or(names[id], id)
			l.g.Resources = append(l.g.Resources, harvest.Record(id, name, kmsKeyType(l.keys[id]), "aws",
				map[string]string{"key_id": id}))
			l.g.Resources = append(l.g.Resources, grants[i]...)
			l.parts.add("grants of key "+id, grantErrs[i])
		}
	}
	return nil
}

// grants returns the grants of the key keyID as aws_kms_grant, imported as
// <the grant's key ID>:<grant ID>, where the grant's key ID is the key's
// ARN, with the principal the grant gives to as grantee_principal, by which
// the default selection leaves out the grants AWS services make for
// themselves (see awsServiceGrantee). A grant is named after its name, or
// else after the key, keyName. A failure to list them ends the key's
// grants there: it returns those listed so far, with the error, and the
// key itself stays listed.
func (l *kmsLister) grants(keyID, keyName string) ([]harvest.Resource, error) {
	var resources []harvest.Resource
	pages := kms.NewListGrantsPaginator(l.svc, &kms.ListGrantsInput{KeyId: aws.String(keyID)}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(l.g.Context())
		if err != nil {
			return resources, err
		}
		for _, grant := range page.Grants {
			id := aws.ToString(grant.KeyId) + ":" + aws.ToString(grant.GrantId)
			attributes := map[string]string{}
			if grantee := cmp.Or(aws.ToString(grant.GranteePrincipal), aws.ToString(grant.GranteeServicePrincipal)); grantee != "" {
				attributes["grantee_principal"] = grantee
			}
			resources = append(resources, harvest.Record(id, cmp.Or(aws.ToString(grant.Name), keyName), "aws_kms_grant", "aws", attributes))
		}
	}
	return resources, nil
}

// listAliases returns the aliases of listable keys, which List records as
// aws_kms_alias, imported by their name. An alias that points at no key is
// left out, and so is an AWS managed alias, alias/aws/<service>, without
// describing its key: only AWS managed keys have those. Its key is
// remembered as AWS managed, so that listing the keys doesn't describe it
// either.
func (l *kmsLister) listAliases() ([]types.AliasListEntry, error) {
	var aliases []types.AliasListEntry
	pages := kms.NewListAliasesPaginator(l.svc, &kms.ListAliasesInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(l.g.Context())
		if err != nil {
			return nil, err
		}
		var customer []types.AliasListEntry
		var keyIDs []string
		for _, alias := range page.Aliases {
			if alias.TargetKeyId == nil {
				continue
			}
			if strings.HasPrefix(aws.ToString(alias.AliasName), awsManagedAliasPrefix) {
				if _, described := l.keys[*alias.TargetKeyId]; !described {
					l.keys[*alias.TargetKeyId] = &types.KeyMetadata{KeyId: alias.TargetKeyId, KeyManager: types.KeyManagerTypeAws}
				}
				continue
			}
			customer = append(customer, alias)
			keyIDs = append(keyIDs, *alias.TargetKeyId)
		}
		l.describeAll(keyIDs)
		for _, alias := range customer {
			if kmsKeyListable(l.keys[*alias.TargetKeyId]) {
				aliases = append(aliases, alias)
			}
		}
	}
	return aliases, nil
}

// awsManagedAliasPrefix starts the aliases of the keys AWS manages for its
// services, such as alias/aws/s3; no one else can create one.
const awsManagedAliasPrefix = "alias/aws/"

// describeAll describes the keys of keyIDs not described yet,
// resourceParallel at a time, each once, and remembers their metadata in
// l.keys: nil for a key that can't be described, whose failure it gathers,
// but for a key deleted since it was listed. Failures are remembered too,
// so that a key's failure is reported once, and they are gathered in the
// order of keyIDs.
func (l *kmsLister) describeAll(keyIDs []string) {
	var todo []string
	seen := map[string]bool{}
	for _, id := range keyIDs {
		if _, described := l.keys[id]; !described && !seen[id] {
			seen[id] = true
			todo = append(todo, id)
		}
	}
	metadata := make([]*types.KeyMetadata, len(todo))
	errs := make([]error, len(todo))
	inParallel(len(todo), resourceParallel, func(i int) {
		var key *kms.DescribeKeyOutput
		key, errs[i] = l.svc.DescribeKey(l.g.Context(), &kms.DescribeKeyInput{KeyId: aws.String(todo[i])})
		if errs[i] == nil {
			metadata[i] = key.KeyMetadata
		}
	})
	for i, id := range todo {
		l.keys[id] = metadata[i]
		l.parts.add("key "+id, errs[i])
	}
}

// kmsKeyType returns the Terraform type of a key with metadata: Terraform
// has one for each origin of the key material and for a multi-Region
// replica, and aws_kms_key, which takes only keys whose material KMS made
// and that aren't replicas, refuses to read the others. A replica key is an
// aws_kms_replica_key, or an aws_kms_replica_external_key when its
// material was imported; another key with imported material is an
// aws_kms_external_key.
func kmsKeyType(metadata *types.KeyMetadata) string {
	replica := metadata.MultiRegionConfiguration != nil &&
		metadata.MultiRegionConfiguration.MultiRegionKeyType == types.MultiRegionKeyTypeReplica
	external := metadata.Origin == types.OriginTypeExternal
	switch {
	case replica && external:
		return "aws_kms_replica_external_key"
	case replica:
		return "aws_kms_replica_key"
	case external:
		return "aws_kms_external_key"
	}
	return "aws_kms_key"
}

// kmsKeyListable reports whether a key with metadata is one to list: a
// customer manages it and it isn't scheduled for deletion, as a key or as a
// replica. Nil metadata, a key that couldn't be described, isn't listable.
func kmsKeyListable(metadata *types.KeyMetadata) bool {
	if metadata == nil || metadata.KeyManager != types.KeyManagerTypeCustomer {
		return false
	}
	switch metadata.KeyState {
	case types.KeyStatePendingDeletion, types.KeyStatePendingReplicaDeletion:
		return false
	}
	return true
}
