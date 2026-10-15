// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"log"

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
// on its way out can't be imported for long. Failing to list keys or
// aliases fails the service; a key or an alias whose key can't be
// described is logged and left out.
func (g *KmsLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	lister := &kmsLister{g: g, svc: kms.NewFromConfig(config), keys: map[string]*types.KeyMetadata{}}
	if err := lister.addKeys(); err != nil {
		return err
	}
	return lister.addAliases()
}

// kmsLister holds what one listing of the KMS service shares: the client,
// and the keys described so far, so that an alias of a key listed earlier
// doesn't describe that key again.
type kmsLister struct {
	g    *KmsLister
	svc  *kms.Client
	keys map[string]*types.KeyMetadata
}

// addKeys records every listable key as an aws_kms_key, imported by its ID,
// which it also records as the key_id attribute, followed by its grants.
func (l *kmsLister) addKeys() error {
	pages := kms.NewListKeysPaginator(l.svc, &kms.ListKeysInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(l.g.Context())
		if err != nil {
			return err
		}
		for _, key := range page.Keys {
			metadata := l.describe(aws.ToString(key.KeyId))
			if !kmsKeyListable(metadata) {
				continue
			}
			id := aws.ToString(key.KeyId)
			l.g.Resources = append(l.g.Resources, harvest.Record(id, id, "aws_kms_key", "aws",
				map[string]string{"key_id": id}))
			l.addGrants(id)
		}
	}
	return nil
}

// addGrants records the grants of the key keyID as aws_kms_grant, imported
// as <the grant's key ID>:<grant ID>, where the grant's key ID is the key's
// ARN. A failure to list them is logged and ends the key's grants there:
// the key itself stays listed.
func (l *kmsLister) addGrants(keyID string) {
	pages := kms.NewListGrantsPaginator(l.svc, &kms.ListGrantsInput{KeyId: aws.String(keyID)}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(l.g.Context())
		if err != nil {
			log.Printf("aws: kms: can't list the grants of key %s: %v", keyID, err)
			return
		}
		for _, grant := range page.Grants {
			id := aws.ToString(grant.KeyId) + ":" + aws.ToString(grant.GrantId)
			l.g.Resources = append(l.g.Resources, harvest.RecordBare(id, id, "aws_kms_grant", "aws"))
		}
	}
}

// addAliases records the aliases of listable keys as aws_kms_alias,
// imported by their name. An alias that points at no key is left out.
func (l *kmsLister) addAliases() error {
	pages := kms.NewListAliasesPaginator(l.svc, &kms.ListAliasesInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(l.g.Context())
		if err != nil {
			return err
		}
		for _, alias := range page.Aliases {
			if alias.TargetKeyId == nil || !kmsKeyListable(l.describe(*alias.TargetKeyId)) {
				continue
			}
			name := aws.ToString(alias.AliasName)
			l.g.Resources = append(l.g.Resources, harvest.RecordBare(name, name, "aws_kms_alias", "aws"))
		}
	}
	return nil
}

// describe returns the metadata of the key keyID, or nil when it can't be
// described, which it logs. Successful answers are remembered.
func (l *kmsLister) describe(keyID string) *types.KeyMetadata {
	if metadata, ok := l.keys[keyID]; ok {
		return metadata
	}
	key, err := l.svc.DescribeKey(l.g.Context(), &kms.DescribeKeyInput{KeyId: aws.String(keyID)})
	if err != nil {
		log.Printf("aws: kms: can't describe key %s; leaving it out: %v", keyID, err)
		return nil
	}
	l.keys[keyID] = key.KeyMetadata
	return key.KeyMetadata
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
