package io.github.onurbaydas.virtomir;

import com.sun.net.httpserver.HttpServer;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;

import static org.junit.jupiter.api.Assertions.*;

class VirtoMirClientTest {
    private static final String U = "6f1c2a5e-0000-4000-8000-000000000001";
    private HttpServer srv;
    private final List<String> seen = new ArrayList<>();
    private final List<String> auth = new ArrayList<>();
    private final AtomicInteger status = new AtomicInteger(200);
    private final AtomicReference<String> reply = new AtomicReference<>("");

    private String start() throws IOException {
        srv = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        srv.createContext("/", ex -> {
            ex.getRequestBody().readAllBytes();
            seen.add(ex.getRequestMethod() + " " + ex.getRequestURI());
            auth.add(ex.getRequestHeaders().getFirst("Authorization"));
            byte[] b = reply.get().getBytes(StandardCharsets.UTF_8);
            ex.sendResponseHeaders(status.get(), b.length == 0 ? -1 : b.length);
            if (b.length > 0) ex.getResponseBody().write(b);
            ex.close();
        });
        srv.start();
        return "http://127.0.0.1:" + srv.getAddress().getPort() + "/";
    }

    @AfterEach
    void stop() {
        if (srv != null) srv.stop(0);
    }

    @Test
    void listsMachinesWithTheToken() throws IOException {
        reply.set("{\"domains\":[{\"uuid\":\"" + U + "\",\"name\":\"web\",\"state\":\"running\",\"vcpus\":2,\"maxMemoryKiB\":2097152,\"autostart\":true,\"extra\":1}]}");
        var c = VirtoMirClient.builder(start()).token("tok").build();
        var list = c.listMachines();
        assertEquals(1, list.size());
        assertEquals("web", list.get(0).name());
        assertTrue(list.get(0).isRunning());
        assertEquals("Bearer tok", auth.get(0));
        assertEquals("GET /api/v1/domains", seen.get(0));
        reply.set("{\"domains\":null}");
        assertTrue(c.listMachines().isEmpty());
        assertTrue(c.findMachine(U.toUpperCase()).isEmpty());
    }

    @Test
    void routesThroughAClusterMember() throws IOException {
        var c = VirtoMirClient.builder(start()).token("tok").host("hv2").build();
        status.set(202);
        c.power(U, PowerAction.FORCE_OFF);
        status.set(204);
        c.setAutostart(U, false);
        c.revertSnapshot(U, "before upgrade", false);
        c.deleteMachine(U, true);
        assertEquals(List.of(
                "POST /api/v1/hosts/hv2/domains/" + U + "/actions/force-off",
                "PUT /api/v1/hosts/hv2/domains/" + U + "/autostart",
                "POST /api/v1/hosts/hv2/domains/" + U + "/snapshots/before%20upgrade/revert?safety=false",
                "DELETE /api/v1/hosts/hv2/domains/" + U + "?storage=remove"), seen);
    }

    @Test
    void createsASnapshot() throws IOException {
        status.set(201);
        reply.set("{\"name\":\"before-upgrade\"}");
        var c = VirtoMirClient.builder(start()).build();
        assertEquals("before-upgrade", c.createSnapshot(U, "before-upgrade", ""));
    }

    @Test
    void anErrorAnswerCarriesTheMessage() throws IOException {
        status.set(404);
        reply.set("{\"error\":\"machine not found\"}");
        var c = VirtoMirClient.builder(start()).build();
        var e = assertThrows(VirtoMirException.class, () -> c.getMachine(U));
        assertTrue(e.isNotFound());
        assertTrue(e.getMessage().contains("machine not found"));
    }

    @Test
    void refusesABadUrl() {
        assertThrows(IllegalArgumentException.class, () -> VirtoMirClient.builder("hv1:8420"));
    }
}
