#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Fails when a deny list in the permission templates names what its cloud
# doesn't: an action AWS's service authorization reference doesn't define
# in permissions/aws/deny-data-reads.json, or a permission Google's deny
# policies don't support in permissions/google/deny-data-reads.json. A
# misspelt action denies nothing, and the templates' own tests can't tell.
#
# With --readonlyaccess, it also fails when the default version of AWS's
# ReadOnlyAccess policy isn't the one the AWS list was last checked
# against: ReadOnlyAccess grows with new services, and a data read it gains
# is allowed until the list denies it. Check the new version's actions for
# data reads, deny them, then update readonlyaccess_version below.
#
# Everything it reads is public documentation, fetched without
# credentials. The Permission names workflow runs it weekly and on pull
# requests that change the lists.
#
# Usage: .github/scripts/check-permission-names.sh [--readonlyaccess]
set -euo pipefail

# ReadOnlyAccess's default version when the AWS list was last checked.
readonlyaccess_version=v190

# Actions the AWS list denies that the service reference doesn't define
# (yet): logs:GetLogObject is the action of the GetLogObject API, which
# returns large log objects; if it is authorized under its own name,
# ReadOnlyAccess's logs:Get* grants it.
undefined_aws_actions=(logs:GetLogObject)

aws_list=permissions/aws/deny-data-reads.json
google_list=permissions/google/deny-data-reads.json

cd "$(dirname "$0")/../.."
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
status=0

fetch() {
  curl -fsSL --retry 3 --retry-all-errors -o "$2" "$1"
}

# AWS: each service's actions, from the service reference's own file.
fetch https://servicereference.us-east-1.amazonaws.com/ "$work/services.json"
jq -r '.Statement[].Action[]' "$aws_list" > "$work/aws-actions"
cut -d: -f1 "$work/aws-actions" | sort -u > "$work/aws-services"
while read -r service; do
  url=$(jq -r --arg service "$service" '.[] | select(.service == $service) | .url' "$work/services.json")
  if [ -z "$url" ]; then
    echo "$aws_list: AWS's service reference has no service $service"
    status=1
    continue
  fi
  fetch "$url" "$work/$service.json"
  jq -r '.Actions[].Name' "$work/$service.json" > "$work/$service.names"
done < "$work/aws-services"
while IFS=: read -r service action; do
  if [ -f "$work/$service.names" ] && ! grep -qxF "$action" "$work/$service.names" \
    && [[ " ${undefined_aws_actions[*]} " != *" $service:$action "* ]]; then
    echo "$aws_list: AWS's service reference has no action $service:$action"
    status=1
  fi
done < "$work/aws-actions"

# Google: the permissions deny policies support, as their page lists them.
fetch https://docs.cloud.google.com/iam/docs/deny-permissions-support "$work/google.html"
grep -oE '[a-z0-9-]+\.googleapis\.com/[A-Za-z0-9.]+' "$work/google.html" | sort -u > "$work/google-permissions"
while read -r permission; do
  if ! grep -qxF "$permission" "$work/google-permissions"; then
    echo "$google_list: Google's deny policies don't support $permission"
    status=1
  fi
done < <(jq -r '.[]' "$google_list")

if [ "${1-}" = --readonlyaccess ]; then
  fetch https://docs.aws.amazon.com/aws-managed-policy/latest/reference/ReadOnlyAccess.html "$work/readonlyaccess.html"
  version=$(grep -oE 'Policy version:</b> v[0-9]+' "$work/readonlyaccess.html" | grep -oE 'v[0-9]+$' || true)
  if [ -z "$version" ]; then
    echo "ReadOnlyAccess: no policy version on its page; the page may have changed"
    status=1
  elif [ "$version" != "$readonlyaccess_version" ]; then
    echo "ReadOnlyAccess: the default version is $version, but $aws_list was last checked against $readonlyaccess_version."
    echo "Check its actions for data reads (https://docs.aws.amazon.com/aws-managed-policy/latest/reference/ReadOnlyAccess.html), deny them, then update readonlyaccess_version in $0."
    status=1
  fi
fi

exit "$status"
