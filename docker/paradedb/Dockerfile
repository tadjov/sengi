# ParadeDB's last PostgreSQL 18.6 image ships pgvector 0.8.6; tiger ships 0.8.7.
# The +2 build needs postgresql-18 > 18.7 and would upgrade the server.
FROM paradedb/paradedb:0.26.6-pg18@sha256:4df3bcf9f19b32821446967f7874d1a40bee8b9550184cc0d70e93f51ee8af36
RUN apt-get update \
 && apt-get install -y --no-install-recommends postgresql-18-pgvector=0.8.7-1.pgdg13+1 \
 && rm -rf /var/lib/apt/lists/*
