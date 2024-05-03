# Postgres as a Service

## Architecture

### Control Plane Overview

![overview](diagrams/architecture.drawio.svg "Architecture Overview")

### cloud-api

Self-service REST endpoint for customers. Needs to be extended to accept DBaaS calls and convert them to calls to the `s3-api` and `billing-api` accordingly. Writes/updates/deletes the `Postgres` CRs from the incoming Rest requests.

### s3-api

Internal abstraction layer for the creation, management and deletion of S3 storage. Already implements all the features required for this endeavor.

### accounting-api

Internal abstraction layer for department-wide billing of cloud-native services. Needs to be extended to account for the new DBaaS product.

### postgreslet

Newly introduced DBMS-specific (or, more specifically, DBMS-operator specific) abstraction layer for the creation, management and deletion of Postgres instances. Watches for `Postgres` CRDs in the control-plane Kubernetes Server. Depending on the configuration specified in the `PostgresProfile`, it will reconcile the `Postgres` database instances in this cluster. This is done by selecting only these `postgres` resources which are configured to be created in this partition. Additionally a postgres partition for a dedicated tenant is possible, the postgreslet will then also check if the given tenant in the postgres resource matches the one in the PostgresProfile. RBAC in the control-plane cluster restricts access to the Postgres CRD only. One instance per service cluster/partition.

### postgres-operator

Existing operator that does the heavy lifting, [implemented and maintained by Zalando](https://github.com/zalando/postgres-operator/). Watches for custom resources created/updated by our `postgreslet`. Probably one instance per project and service cluster (due to restrictions in the configuration of the backup storage backend).

## CLI mock

### create database

Creation of a pg database will create a superuser in the database and the end user is capable of creating his users on demand.
Creation of additional users might be implemented in the same manner as we already do for s3.

```bash
 ~ $ cloudctl database pg create --help
creates a managed database

Usage:
   cloudctl database pg create [flags]

Flags:
      --auto-update boolean           enable or disable automatic minor updates
      --cpu int                       CPU size per DB node. Memory size will be number of CPUs x 4GiB
      --maintenance-window            maintenance window for automatic minor updates
      --name string                   name of the database (immutable) [required]
  -p, --partition string              name of database partition where this user is in (immutable) [required]
      --project string                id of the project that the database belongs to (immutable) [required]
      --replicas int                  number of nodes in the database cluster (1 or 3)
      --retention string              backup retention
      --shared-buffer string          size of the shared buffer to use
      --sourcePrefixes [] string      List of networks that are allowed to connect (cidr) [required]
      --storage-size string           storage size per database pod, only enlarge is supported [required]
  -t, --tenant string                 tenant of the database, defaults to logged in tenant (immutable)
      --version string                (semantic) version of the database to create, e.g. 11 (immutable, upgrade via clone)
```

```bash
 ~ $ cloudctl database pg create \
       --name my-db \
       --project 5820c4e7-fbd4-4e4b-a40b-2b83eb34bbe1 \
       --partition fra-equ01 \
       --version 11 \
       --auto-updates true \
       --maintenance-window Sun, 0500-0700
       --cpu 1 \
       --replicas 1 \
       --storage-size 100g \
       --retention 7d \
       --shared-buffer small \
       --sourcePrefixes "127.0.0.1/32, 127.192.168.1/32"

UUID                                  TYPE   NAME    CPU    MEMORY    RETENTION    REPLICAS
00000000-0000-0000-0000-ac1f6bd39026  pg     my-db   1      4         7d           1/1
```

#### TODO

* Configuration of
  * Backup
  * Replication
  * Storage (quality, size)
    * size can be either elastic (automatically resized on customer demands) or statically defined by the customer
* db type as creation-time cli argument (`--type pg`) or part of the command (`cloudctl database pg list`)
* `db-` prefix for dbms specific configuration, like encoding, default name, shm size, user etc.?
* **SSL by default!!!**
* cpu und memory als t-shirt size
  * wo werden die hinterlegt?
* db-encoding ist fix utf-a
* db-name und db-user werden nicht implemntiert
* type ist subcommand von database create
* ssl certificate im describe ?
* TODO ingress / lb
* TODO prometheus/grafana credentials
* ASYNC vs SYNC => currently, only one sync node is supported, so if replicas==3, we create one SYNC and one ASYNC node
* Tenant-owned partitions: Dedicated service clusters for certain customers instead of managed services within customer clusters.
* No Grafana (only Prometheus federation). A dedicated Grafana/Prometheus will probably become a new, dedicated product.

### describe database

```bash
 ~ $ cloudctl database pg describe 00000000-0000-0000-0000-ac1f6bd39026

---
kind: database.fits.cloud/postgres
version: v1
objectmeta:
    id: 00000000-0000-0000-0000-ac1f6bd39026
name: my-db
project: 5820c4e7-fbd4-4e4b-a40b-2b83eb34bbe1
partitionid: fra-equ01
type: pg # FIXME can be removed?
changed: "2020-11-11T10:39:51.778Z"
created: "2020-08-04T10:35:07.597Z"
database:
    version: 11
    encoding: utf-8
    shared_buffer: small # TODO show actual values?
pods:
    replicas: 1
    size: small
    limits: # TODO show actual values?
        cpu: 1
        mem: 1GB
    storage:
        class: nvme
        size: 100GBS
backup:
    retention: 7d
    # TODO one database-backup-s3 per __database__ project ?
    bucket: asdfsadf
    secret_ref: backup-secret
connection:
    host: pg.123ed1-my-db.dbaas.fits.cloud
    ip: 1.2.3.4 # for netpols
    port: 4711
    credentials:
        ca.crt: ""
        client.crt: ""
        client.key: ""
users:
    - name: root
        secret_ref: users-secret
status: RUNNING|DEGRADED|FAULTY|WARNING
monitoring:
    federation:
        metrics_path: /federation
        targets:
            - prometheus.123ed1-my-db.dbaas.fits.cloud:32907
```

#### TODO

* requested by Uli: tuple of ip/port and federation url for his metrics

### update database

```bash
 ~ $ cloudctl database pg update --help
update a managed databases configuration

Usage:
   cloudctl database pg update [flags] ID

Flags:
      --auto-update boolean           enable or disable automatic minor updates
      --cpu int                       CPU size per DB node. Memory size will be number of CPUs x 4GiB
      --maintenance-window            maintenance window for automatic minor updates
  -p, --partition string              name of the partition of the source database (immutable)
      --project string                id of the project of the source database (immutable)
      --replicas int                  number of nodes in the database cluster (1 or 3)
      --retention string              backup retention
      --shared-buffer string          size of the shared buffer to use
      --sourcePrefixes [] string      List of networks that are allowed to connect (cidr)
      --storage-size string           storage size per database pod
  -t, --tenant string                 tenant of the database, defaults to logged in tenant (immutable)
```

```bash
 ~ $ cloudctl database pg update 00000000-0000-0000-0000-ac1f6bd39026 \
       --replicas 3

UUID                                  TYPE   NAME    CPU    MEMORY    RETENTION    REPLICAS
00000000-0000-0000-0000-ac1f6bd39026  pg     my-db   1      4         7d           1/3
```

#### TODO

* admin password change/reset?
* minor updates automatically
* major upgrades via `clone`

### apply database configuration

```bash
 ~ $ cloudctl database pg apply --help
applies a given JSON file and updates an existing database accordingly. See cloudctl database pg update for a list of valid keys.

Usage:
   cloudctl database pg apply [flags] [ID]

Flags:
  -f  --filename string               name of the file that contains the configuration to apply
  -p, --partition string              name of the partition of the source database (immutable)
      --project string                id of the project of the source database (immutable)
  -t, --tenant string                 tenant of the database, defaults to logged in tenant (immutable)
```


```bash
 ~ $ cat my-db.json
 {
     replicas: 1,
     retention: 8d
 }
 ~ $ cloudctl database pg update 00000000-0000-0000-0000-ac1f6bd39026 \
       --filename my-db.json

UUID                                  TYPE   NAME    CPU    MEMORY    RETENTION    REPLICAS
00000000-0000-0000-0000-ac1f6bd39026  pg     my-db   1      4         8d           1/1
```

#### TODO

* apply json key-value-pair. See `update` for a list of valid keys.

### clone database

```bash
 ~ $ cloudctl database pg clone --help
creates a new managed database by copying the configuration of an existing database and importing the latest backup of that source database

Usage:
   cloudctl database pg clone [flags] ID

Flags:
      --auto-update boolean           enable or disable automatic minor updates (defaults to the value of the existing db)
      --cpu int                       CPU size per DB node. Memory size will be number of CPUs x 4GiB (defaults to the value of the existing db)
      --maintenance-window            maintenance window for automatic minor updates (defaults to the value of the existing db)
      --name string                   name of the new database to create (immutable) [required]
  -p, --partition string              name of the partition of the source database (immutable)
      --project string                id of the project of the source database (immutable)
      --replicas int                  number of nodes in the new database cluster (1 or 3, defaults to the value of the existing db)
      --retention string              backup retention (defaults to the value of the existing db)
      --shared-buffer string          size of the shared buffer to use (defaults to the value of the existing db)
      --sourcePrefixes [] string      List of networks that are allowed to connect (cidr)
  -t, --tenant string                 tenant of the database, defaults to logged in tenant (immutable)
      --version string                (semantic) version of the database to create, e.g. 11 (no downgrade, but possibly an upgrade)
```

```bash
 ~ $ cloudctl database pg clone 00000000-0000-0000-0000-ac1f6bd39026 \
       --name my-clone \
       --version 12

UUID                                  TYPE   NAME      CPU    MEMORY    RETENTION    REPLICAS
00000000-0000-0000-0000-62093dv6f1ca  pg     my-clone   1      1         7d           1
```

#### TODO

* major upgrades
* restore
* point in time restore

### delete databases

```bash
 ~ $ cloudctl database delete --help
deletes a managed database

Usage:
   cloudctl database pg delete [flags] ID

Flags:
  -p, --partition string   name of database partition where this user is in
      --project string     id of the project that the database belongs to
  -t, --tenant string      tenant of the database, defaults to logged in tenant
```

#### TODO

* does delete remove the backups, or will those be kept according to the retention period? => no automatic deletion of backups but human readable hint

### list databases


```bash
 ~ $ cloudctl database pg ls --help
deletes a managed database

Usage:
   cloudctl database [pg] ls [flags] [ID]

Flags:
  -p, --partition string   name of database partition where this user is in
      --project string     id of the project that the database belongs to
  -t, --tenant string      tenant of the database, defaults to logged in tenant
```

```bash
cloudctl database ls
UUID                                  TYPE   NAME    CPU    MEMORY    RETENTION    REPLICAS STORAGE
00000000-0000-0000-0000-ac1f6bd39026  pg     my-db   1      1         7d           1        4/50
00000000-0000-0000-0000-ac1f6bd39027  pg     my-db2  1      1         7d           1        1/50
00000000-0000-0000-0000-ac1f6bd39028  pg     my-db3  1      1         7d           1        49/50
```

### database billing

```bash
cloudctl billing database

TENANT  PROJECTID                               PARTITION       TYPE NAME    STORAGE(GB*H) CPU*H MEMORY(GB*H) LIFETIME
fits    cd4eac58-46a5-4a31-b59f-2ec207baa817    fra-equ01       pg   my-db   1244          100   299          21d3h
```

#### TODO

* Traffic
* IOPS

## Noch zu beschreiben

* create / update / delete / apply / describe
* Ausgabe von `billing database` und damit welche Eigenschaften auf der Rechnung stehen, Einheiten !
* alle Felder mit möglichen werten, welche sind Read-Only
* Zusammenhang Project, Operator, Database
* Nur an einem Standort
* Beschreibung der Komponenten und deren Verantwortung
  * cloud-api
  * s3-api
  * accounting-api
  * database-api
  * postgreslet
  * postgres-operator

