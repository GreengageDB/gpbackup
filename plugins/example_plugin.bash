#!/bin/bash
set -e

setup_plugin_for_backup(){
  echo "setup_plugin_for_backup $1 $2 $3 $4" >> /tmp/plugin_out.txt
  if [[ "$3" = "coordinator" || "$3" = "master" ]]
    then echo "setup_plugin_for_backup was called for scope = $3" >> /tmp/plugin_out.txt
  elif [ "$3" = "segment_host" ]
    then echo "setup_plugin_for_backup was called for scope = segment_host" >> /tmp/plugin_out.txt
  elif [ "$3" = "segment" ]
    then echo "setup_plugin_for_backup was called for scope = segment" >> /tmp/plugin_out.txt
  fi
  timestamp_dir=`basename "$2"`
  timestamp_day_dir=${timestamp_dir%??????}
  mkdir -p /tmp/plugin_dest/$timestamp_day_dir/$timestamp_dir
}

setup_plugin_for_restore(){
  echo "setup_plugin_for_restore $1 $2 $3 $4" >> /tmp/plugin_out.txt
  if [[ "$3" = "coordinator" || "$3" = "master" ]]
    then echo "setup_plugin_for_restore was called for scope = $3" >> /tmp/plugin_out.txt
  elif [ "$3" = "segment_host" ]
    then echo "setup_plugin_for_restore was called for scope = segment_host" >> /tmp/plugin_out.txt
  elif [ "$3" = "segment" ]
    then echo "setup_plugin_for_restore was called for scope = segment" >> /tmp/plugin_out.txt
  fi
}

cleanup_plugin_for_backup(){
  echo "cleanup_plugin_for_backup $1 $2 $3 $4" >> /tmp/plugin_out.txt
  if [[ "$3" = "coordinator" || "$3" = "master" ]]
    then echo "cleanup_plugin_for_backup was called for scope = $3" >> /tmp/plugin_out.txt
  elif [ "$3" = "segment_host" ]
    then echo "cleanup_plugin_for_backup was called for scope = segment_host" >> /tmp/plugin_out.txt
  elif [ "$3" = "segment" ]
    then echo "cleanup_plugin_for_backup was called for scope = segment" >> /tmp/plugin_out.txt
  fi
}

cleanup_plugin_for_restore(){
  echo "cleanup_plugin_for_restore $1 $2 $3 $4" >> /tmp/plugin_out.txt
  if [[ "$3" = "coordinator" || "$3" = "master" ]]
    then echo "cleanup_plugin_for_restore was called for scope = $3" >> /tmp/plugin_out.txt
  elif [ "$3" = "segment_host" ]
    then echo "cleanup_plugin_for_restore was called for scope = segment_host" >> /tmp/plugin_out.txt
  elif [ "$3" = "segment" ]
    then echo "cleanup_plugin_for_restore was called for scope = segment" >> /tmp/plugin_out.txt
  fi
}

restore_file() {
  echo "restore_file $1 $2" >> /tmp/plugin_out.txt
  filename=`basename "$2"`
  timestamp_dir=`basename $(dirname "$2")`
  timestamp_day_dir=${timestamp_dir%??????}
	cat /tmp/plugin_dest/$timestamp_day_dir/$timestamp_dir/$filename > $2
}

backup_file() {
  echo "backup_file $1 $2" >> /tmp/plugin_out.txt
  filename=`basename "$2"`
  timestamp_dir=`basename $(dirname "$2")`
  timestamp_day_dir=${timestamp_dir%??????}
	cat $2 > /tmp/plugin_dest/$timestamp_day_dir/$timestamp_dir/$filename
}

backup_data() {
  echo "backup_data $1 $2" >> /tmp/plugin_out.txt
  filename=`basename "$2"`
  timestamp_dir=`basename $(dirname "$2")`
  timestamp_day_dir=${timestamp_dir%??????}
	cat - > /tmp/plugin_dest/$timestamp_day_dir/$timestamp_dir/$filename
}

restore_data() {
  echo "restore_data $1 $2" >> /tmp/plugin_out.txt
  filename=`basename "$2"`
  timestamp_dir=`basename $(dirname "$2")`
  timestamp_day_dir=${timestamp_dir%??????}
  if [ -e "/tmp/GPBACKUP_PLUGIN_LOG_TO_STDERR" ] ; then
    echo 'Some plugin warning' >&2
  elif [ -e "/tmp/GPBACKUP_PLUGIN_DIE" -a "$GPBACKUP_PLUGIN_DIE_ON_OID" = "" ] ; then
    exit 1
  elif [[ -e "/tmp/GPBACKUP_PLUGIN_DIE" && "$filename" == *"$GPBACKUP_PLUGIN_DIE_ON_OID"* ]] ; then
    # sleep a while for test purposes - to let gprestore start COPY commands
    sleep 5
    exit 1
  fi
	cat /tmp/plugin_dest/$timestamp_day_dir/$timestamp_dir/$filename
}

restore_data_subset() {
  echo "restore_data_subset $1 $2 $3" >> /tmp/plugin_out.txt
  filename=`basename "$2"`
  timestamp_dir=`basename $(dirname "$2")`
  timestamp_day_dir=${timestamp_dir%??????}
  cat "$3" | grep -oP " (\d+) (\d+)" | while read -r offset_start offset_end; do
    dd if="/tmp/plugin_dest/$timestamp_day_dir/$timestamp_dir/$filename" ibs=1 skip="$offset_start" count="$(($offset_end-$offset_start))" 2>/dev/null
  done
}

delete_backup() {
  echo "delete_backup $1 $2" >> /tmp/plugin_out.txt
  timestamp_day_dir=${2%??????}
  rm -rf /tmp/plugin_dest/$timestamp_day_dir/$2
  if [ -z "$(ls -A /tmp/plugin_dest/$timestamp_day_dir/)" ] ; then
    rm -rf /tmp/plugin_dest/$timestamp_day_dir
  fi

}

# utility/debugging function to maintain parity with ddboost and s3 plugin
list_directory() {
  echo "list_directory $1 /tmp/plugin_dest" >> /tmp/plugin_out.txt
  ls /tmp/plugin_dest
}

# utility/debugging function to maintain parity with ddboost and s3 plugin
delete_directory() {
  echo "delete_directory $1 /tmp/plugin_dest" >> /tmp/plugin_out.txt
  rm -rf /tmp/plugin_dest
}

plugin_api_version(){
  echo "0.5.0"
  echo "0.5.0" >> /tmp/plugin_out.txt
}

--version(){
  echo "example_plugin version 1.1.0"
  echo "example_plugin version 1.1.0" >> /tmp/plugin_out.txt
}

"$@"
