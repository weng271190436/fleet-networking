FROM busybox:1.37.0
COPY --chmod=0555 test/e2e/afdprivatelink/echo/entrypoint.sh /entrypoint.sh
RUN mkdir -p /www && chown 65532:65532 /www
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
