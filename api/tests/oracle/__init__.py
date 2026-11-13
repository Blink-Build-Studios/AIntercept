"""The v6 oracle (docs/PIPELINE.md, Testing): today's Python derivation, frozen at v6 step 2.

A faithful copy of what turns the v1 layout (`aintercept v1layout`: each key's commits and the
merge's partitions) into the API-shaped tables: v1.V1Store's file picking and buckets.BucketStore's
newest-row fold, session group, Codex title runs and table build (store.py), state_sql, stream_sql,
person_sql, sql/prompts.sql and sql/session_rows.sql, and the fact tables' shapes and the drawer's
views (tables.py). Later steps compare the Go views against it (harness.py); it never changes to
follow production, and goes in step 11 with v1layout's v1 output.
"""
