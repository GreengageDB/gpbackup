## How to run tests

6x:
```bash
docker build -t gpbackup:test -f Dockerfile .
docker run --rm -it --sysctl 'kernel.sem=500 1024000 200 4096' gpbackup:test bash -c "ssh-keygen -A && /usr/sbin/sshd && bash /home/gpadmin/go/src/github.com/GreengageDB/gpbackup/ci/scripts/run_gpbackup_tests.bash"
```

7x:
```bash
docker build -t gpbackup:test7x -f Dockerfile --build-arg GPDB_IMAGE=greengagedb/ggdb7_ubuntu:testing .
docker run --rm -it --sysctl 'kernel.sem=500 1024000 200 4096' gpbackup:test7x bash -c "ssh-keygen -A && /usr/sbin/sshd && bash /home/gpadmin/go/src/github.com/GreengageDB/gpbackup/ci/scripts/run_gpbackup_tests.bash"
```
