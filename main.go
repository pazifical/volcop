package main

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

var backupDir = "volcop_backup"

func main() {
	containerID := os.Args[1]

	err := os.Mkdir(backupDir, 0777)
	if errors.Is(err, os.ErrExist) {
	} else if err != nil {
		log.Fatal(err)
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatal(err)
	}
	defer cli.Close()

	err = copyVolumeContent(containerID, cli)
	if err != nil {
		log.Fatal(err)
	}
}

func copyVolumeContent(containerID string, cli *client.Client) error {
	ctx := context.Background()
	inspection, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return err
	}

	copyMounts := make([]mount.Mount, 0)
	for _, m := range inspection.Mounts {
		copyMounts = append(copyMounts, mount.Mount{
			Type:   m.Type,
			Source: m.Name,
			Target: m.Destination,
		})
	}

	log.Printf("Shutting down container %s", containerID)
	err = cli.ContainerStop(ctx, containerID, container.StopOptions{})
	if err != nil {
		return err
	}

	log.Println("Starting container for copying volumes")
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: "alpine",
		Cmd:   []string{"sleep", "5"},
	}, &container.HostConfig{
		Mounts: copyMounts,
	}, nil, nil, "")
	if err != nil {
		return err
	}

	copyContainerID := resp.ID
	log.Printf("Created container %s", copyContainerID)

	err = cli.ContainerStart(ctx, copyContainerID, container.StartOptions{})
	if err != nil {
		return err
	}

	statusCh, errCh := cli.ContainerWait(ctx, copyContainerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	case <-statusCh:
	}

	out, err := cli.ContainerLogs(ctx, copyContainerID, container.LogsOptions{ShowStdout: true})
	if err != nil {
		return err
	}

	stdcopy.StdCopy(os.Stdout, os.Stderr, out)

	log.Println("Copying mount content to TAR files")
	for _, m := range copyMounts {
		readCloser, pathStat, err := cli.CopyFromContainer(ctx, copyContainerID, m.Target)

		directory := filepath.Join(backupDir, m.Target)
		err = os.MkdirAll(directory, pathStat.Mode)
		if err != nil {
			log.Println(err)
			continue
		}

		output, err := os.Create(filepath.Join(directory, "content.tar"))
		if err != nil {
			log.Println(err)
			continue
		}

		_, err = io.Copy(output, readCloser)
		if err != nil {
			log.Println(err)
			continue
		}

		readCloser.Close()
	}

	log.Printf("Shutting down container %s", copyContainerID)
	err = cli.ContainerStop(ctx, copyContainerID, container.StopOptions{})
	if err != nil {
		return err
	}

	log.Printf("Starting container %s back up", containerID)
	err = cli.ContainerStart(ctx, containerID, container.StartOptions{})
	if err != nil {
		return err
	}

	return nil
}
