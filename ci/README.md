## How to run tests

6x:
```bash
docker build -t gpbackup:test -f Dockerfile .
docker run --rm -it --sysctl 'kernel.sem=500 1024000 200 4096' gpbackup:test bash -c "ssh-keygen -A && /usr/sbin/sshd && bash /home/gpadmin/go/src/github.com/GreengageDB/gpbackup/ci/scripts/run_gpbackup_tests.bash"
```

7x:
```bash
docker build -t gpbackup:test7x -f Dockerfile --build-arg GGDB_IMAGE=greengagedb/ggdb7_ubuntu:testing .
docker run --rm -it --sysctl 'kernel.sem=500 1024000 200 4096' gpbackup:test7x bash -c "ssh-keygen -A && /usr/sbin/sshd && bash /home/gpadmin/go/src/github.com/GreengageDB/gpbackup/ci/scripts/run_gpbackup_tests.bash"
```

**NOTE**:
Running all tests requires 11-13 GB, not including the size of the repository
itself, the Docker image, and the Docker container.

## How to run the migration checks

Start Greengage 6 and Greengage 7 clusters with reachable TCP endpoints. Run the fixture harness with the following environment variables.

```bash
GGCHECKMIGRATE_SOURCE_HOST=127.0.0.1 \
GGCHECKMIGRATE_SOURCE_PORT=26000 \
GGCHECKMIGRATE_SOURCE_USER=gpadmin \
GGCHECKMIGRATE_TARGET_HOST=127.0.0.1 \
GGCHECKMIGRATE_TARGET_PORT=27000 \
GGCHECKMIGRATE_TARGET_USER=gpadmin \
GGCHECKMIGRATE_BINARY="$GOPATH/bin/ggcheckmigrate" \
ci/scripts/test-ggcheckmigrate.bash
```

The source and target credentials can be stored in `.pgpass`.
