FROM rockylinux:9
RUN dnf -y install golang libvirt-devel git make && dnf clean all
WORKDIR /src
CMD ["make", "build"]
