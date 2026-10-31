COPY (
  SELECT queryid, calls, total_exec_time, mean_exec_time, max_exec_time,
         rows, shared_blks_hit, shared_blks_read, temp_blks_written,
         blk_read_time, blk_write_time, wal_bytes, query
  FROM pg_stat_statements
  WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
    AND userid = (SELECT oid FROM pg_roles WHERE rolname = 'isureal')
  ORDER BY total_exec_time DESC
) TO STDOUT WITH CSV HEADER;
