// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

#include <stdio.h>
#include <unistd.h>
#include <string.h>
#include <time.h>
#include "unity.h"
#include "../../uplink-cgo.h"
#include "helpers.h"

void TestBucket(void)
{
    char *_err = "";
    char **err = &_err;
    char *bucket_name = getenv("BUCKET_NAME");

    // Open Project
    ProjectRef_t ref_project = OpenTestProject(err);
    TEST_ASSERT_EQUAL_STRING("", *err);

    // TODO: remove duplication
//    uint8_t *enc_key = "bryanssecretkey";
//    EncryptionAccess_t access = NewEncryptionAccess(enc_key);
    uint8_t *enc_key = "abcdefghijklmnopqrstuvwxyzABCDEF";
    Bytes_t key;
    key.bytes = enc_key;
    key.length = strlen((const char *)enc_key);
    EncryptionAccess_t access;
    access.key = &key;

    BucketRef_t ref_bucket = OpenBucket(ref_project, bucket_name, &access, err);
    TEST_ASSERT_EQUAL_STRING("", *err);

    char *object_path = "TestObject";
    uint8_t *data = "test data 123";
    BufferRef_t ref_data = NewBuffer(data);
    // TODO: add assertions for metadata
    UploadOptions_t opts = {
        "text/plain",
        NULL,
        time(NULL),
    };

    UploadObject(ref_bucket, object_path, ref_data, &opts, err);
    TEST_ASSERT_EQUAL_STRING("", *err);
}

int main(int argc, char *argv[])
{
    UNITY_BEGIN();
    RUN_TEST(TestBucket);
    return UNITY_END();
}
