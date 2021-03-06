#!/bin/bash

test_description="Test service + ctl SSL interaction"

config="`pwd`/config/basic_auth"

. lib/test-lib.sh

test_ipfs_init
cleanup test_clean_ipfs
test_cluster_init "$config"
cleanup test_clean_cluster

test_expect_success "prerequisites" '
    test_have_prereq IPFS && test_have_prereq CLUSTER
'

test_expect_success "BasicAuth fails without credentials" '
    id=`cluster_id`
    { test_must_fail ipfs-cluster-ctl id; } | grep -A1 "401" | grep "Unauthorized"
'

test_expect_success "BasicAuth fails with bad credentials" '
    id=`cluster_id`
    { test_must_fail ipfs-cluster-ctl --basic-auth "userwithoutpass:pass" --force-http id; } | grep -A1 "401" | grep "Unauthorized" &&
    { test_must_fail ipfs-cluster-ctl --basic-auth "testuser" --force-http id; } | grep -A1 "401" | grep "Unauthorized" &&
    { test_must_fail ipfs-cluster-ctl --basic-auth "testuser:badpass" --force-http id; } | grep -A1 "401" | grep "Unauthorized" &&
    { test_must_fail ipfs-cluster-ctl --basic-auth "baduser:testpass" --force-http id; } | grep -A1 "401" | grep "Unauthorized" &&
    { test_must_fail ipfs-cluster-ctl --basic-auth "baduser:badpass" --force-http id; } | grep -A1 "401" | grep "Unauthorized"
'

test_expect_success "BasicAuth over HTTP succeeds with CLI flag credentials" '
    id=`cluster_id`
    ipfs-cluster-ctl --basic-auth "userwithoutpass" --force-http id | grep -q "$id" &&
    ipfs-cluster-ctl --basic-auth "testuser:testpass" --force-http id | grep -q "$id"
'

test_expect_success "BasicAuth succeeds with env var credentials" '
    id=`cluster_id`
    export CLUSTER_CREDENTIALS="testuser:testpass"
    ipfs-cluster-ctl --force-http id | egrep -q "$id"
'

test_done
