package build

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/Azure/azure-storage-blob-go/2018-03-28/azblob"
)

type AzureBlobstoreConfig struct {
	Account   string
	Token     string
	Container string
}

func AzureBlobstoreUpload(path string, name string, config AzureBlobstoreConfig) error {
	if *DryRunFlag {
		fmt.Printf("would upload %q to %s/%s/%s\n", path, config.Account, config.Container, name)
		return nil
	}
	credential := azblob.NewSharedKeyCredential(config.Account, config.Token)
	pipeline := azblob.NewPipeline(credential, azblob.PipelineOptions{})

	u, _ := url.Parse(fmt.Sprintf("https://%s.blob.core.windows.net", config.Account))
	service := azblob.NewServiceURL(*u, pipeline)

	container := service.NewContainerURL(config.Container)
	blockblob := container.NewBlockBlobURL(name)

	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()

	_, err = blockblob.Upload(context.Background(), in, azblob.BlobHTTPHeaders{}, azblob.Metadata{}, azblob.BlobAccessConditions{})
	return err
}

func AzureBlobstoreList(config AzureBlobstoreConfig) ([]azblob.BlobItem, error) {
	credential := azblob.NewSharedKeyCredential(config.Account, config.Token)
	pipeline := azblob.NewPipeline(credential, azblob.PipelineOptions{})

	u, _ := url.Parse(fmt.Sprintf("https://%s.blob.core.windows.net", config.Account))
	service := azblob.NewServiceURL(*u, pipeline)

	container := service.NewContainerURL(config.Container)

	res, err := container.ListBlobsFlatSegment(context.Background(), azblob.Marker{}, azblob.ListBlobsSegmentOptions{
		MaxResults: 1024 * 1024 * 1024,
	})
	if err != nil {
		return nil, err
	}
	return res.Segment.BlobItems, nil
}

func AzureBlobstoreDelete(config AzureBlobstoreConfig, blobs []azblob.BlobItem) error {
	if *DryRunFlag {
		for _, blob := range blobs {
			fmt.Printf("would delete %s (%s) from %s/%s\n", blob.Name, blob.Properties.LastModified, config.Account, config.Container)
		}
		return nil
	}
	credential := azblob.NewSharedKeyCredential(config.Account, config.Token)
	pipeline := azblob.NewPipeline(credential, azblob.PipelineOptions{})

	u, _ := url.Parse(fmt.Sprintf("https://%s.blob.core.windows.net", config.Account))
	service := azblob.NewServiceURL(*u, pipeline)

	container := service.NewContainerURL(config.Container)

	for _, blob := range blobs {
		blockblob := container.NewBlockBlobURL(blob.Name)
		if _, err := blockblob.Delete(context.Background(), azblob.DeleteSnapshotsOptionInclude, azblob.BlobAccessConditions{}); err != nil {
			return err
		}
	}
	return nil
}
