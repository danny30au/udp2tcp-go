#
# OpenWrt package Makefile for udp2tcp-go
#
# Drop this into your OpenWrt feeds directory:
#   feeds/packages/net/udp2tcp-go/Makefile
#
# Then run:
#   ./scripts/feeds update -a
#   ./scripts/feeds install -a
#   make menuconfig   # Network → udp2tcp-go
#   make package/udp2tcp-go/compile V=s
#
# Or build it standalone against an existing OpenWrt SDK:
#   cp -r openwrt/ <sdk>/package/udp2tcp-go
#   make package/udp2tcp-go/compile V=s

include $(TOPDIR)/rules.mk

PKG_NAME:=udp2tcp-go
PKG_VERSION:=0.1.0
PKG_RELEASE:=1

PKG_SOURCE_PROTO:=git
PKG_SOURCE_URL:=https://github.com/danny30au/udp2tcp-go.git
PKG_SOURCE_VERSION:=main
PKG_MIRROR_HASH:=skip

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_USE_MIPS16:=0

GO_PKG:=github.com/danny30au/udp2tcp-go
GO_PKG_BUILD_PKG:=github.com/danny30au/udp2tcp-go/cmd/udp2tcp

# Pass version string into the binary via ldflags.
GO_PKG_LDFLAGS_X:=main.version=$(PKG_VERSION)
GO_PKG_LDFLAGS:=-s -w

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/udp2tcp-go
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=VPN
  TITLE:=UDP↔TCP proxy for WireGuard TCP transport
  URL:=https://github.com/danny30au/udp2tcp-go
  DEPENDS:=$(GO_ARCH_DEPENDS)
  MAINTAINER:=thelakesclub@gmail.com
endef

define Package/udp2tcp-go/description
  High-performance multi-core UDP↔TCP proxy with WireGuard-compatible
  2-byte length-prefix framing. Uses SO_REUSEPORT for kernel-level
  distribution across worker goroutines. Zero CGO — static binary.

  Modes:
    UDP→TCP  (client side, alongside wg-userspace/kernel)
    TCP→UDP  (server side, --reverse flag)

  Compatible with wstunnel, wg-tcp-tunnel, and similar TCP transports.
endef

define Package/udp2tcp-go/conffiles
/etc/config/udp2tcp
endef

define Package/udp2tcp-go/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(GO_PKG_BUILD_BIN_DIR)/udp2tcp $(1)/usr/bin/udp2tcp

	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/udp2tcp.init $(1)/etc/init.d/udp2tcp

	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/udp2tcp.config $(1)/etc/config/udp2tcp
endef

$(eval $(call GoBinPackage,udp2tcp-go))
$(eval $(call BuildPackage,udp2tcp-go))
