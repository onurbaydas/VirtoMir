# VirtoMir client for Java

A small client for the [VirtoMir](https://github.com/onurbaydas/VirtoMir) API (`/api/v1`): machines, power, autostart and snapshots, with `request()` for everything else. Java 17+.

## Install

The package is on GitHub Packages. Add the repository (and a `github` server with a token that can `read:packages` in `~/.m2/settings.xml`):

```xml
<repositories>
  <repository>
    <id>github</id>
    <url>https://maven.pkg.github.com/onurbaydas/VirtoMir</url>
  </repository>
</repositories>

<dependency>
  <groupId>io.github.onurbaydas</groupId>
  <artifactId>virtomir-client</artifactId>
  <version>VERSION</version>
</dependency>
```

## Use

Make an API token on the **Account** page of VirtoMir.

```java
var vm = VirtoMirClient.builder("https://hv1:8420").token(System.getenv("VIRTOMIR_TOKEN")).build();
for (Machine m : vm.listMachines()) System.out.println(m.name() + " " + m.state());

vm.findMachine("web").ifPresent(web -> {
    vm.createSnapshot(web.uuid(), "before-upgrade", "");
    vm.power(web.uuid(), PowerAction.REBOOT);
});

JsonNode pools = vm.request("GET", "/api/v1/pools", null);
```

`.host("hv2")` sends every call to a cluster member through this host. For a self-signed host certificate, pass `.httpClient(...)` with an `SSLContext` that trusts it. Errors are `VirtoMirException` with `status()` and the API's message.
