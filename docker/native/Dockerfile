# pgvector/pgvector publishes no PostgreSQL 18.6 image with pgvector 0.8.7, the pair tiger ships.
# The +2 package build depends on postgresql-18 > 18.7 and would upgrade the server.
FROM postgres:18.6-bookworm@sha256:f1ea2a1a4fb7b68d4836be1fdcb8a6466a89b072158bf9d97663600538c294c5
RUN apt-get update \
 && apt-get install -y --no-install-recommends postgresql-18-pgvector=0.8.7-1.pgdg12+1 \
 && rm -rf /var/lib/apt/lists/*
