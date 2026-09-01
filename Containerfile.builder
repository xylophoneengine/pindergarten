FROM quay.io/rockylinux/rockylinux:9
RUN dnf -y install dnf-plugins-core && dnf config-manager --set-enabled crb && dnf -y install golang libvirt-devel git make && dnf clean all
WORKDIR /src
CMD ["make", "build"]
