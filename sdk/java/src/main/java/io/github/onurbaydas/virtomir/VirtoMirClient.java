package io.github.onurbaydas.virtomir;

import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.DeserializationFeature;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

import java.io.IOException;
import java.io.UncheckedIOException;
import java.net.URI;
import java.net.URLEncoder;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Optional;

/**
 * Client for the VirtoMir API (/api/v1). Sign in with an API token made on the Account page.
 *
 * <pre>{@code
 * var vm = VirtoMirClient.builder("https://hv1:8420").token(System.getenv("VIRTOMIR_TOKEN")).build();
 * for (Machine m : vm.listMachines()) System.out.println(m.name() + " " + m.state());
 * }</pre>
 */
public final class VirtoMirClient {
    private static final ObjectMapper JSON = new ObjectMapper()
            .configure(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES, false);

    private final String baseUrl;
    private final String token;
    private final String host;
    private final HttpClient http;

    private VirtoMirClient(Builder b) {
        this.baseUrl = b.baseUrl;
        this.token = b.token;
        this.host = b.host;
        this.http = b.http != null ? b.http
                : HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(30)).build();
    }

    /** baseUrl looks like https://hv1:8420 */
    public static Builder builder(String baseUrl) {
        return new Builder(baseUrl);
    }

    public static final class Builder {
        private final String baseUrl;
        private String token;
        private String host;
        private HttpClient http;

        private Builder(String baseUrl) {
            URI u;
            try {
                u = URI.create(baseUrl);
            } catch (IllegalArgumentException e) {
                u = null;
            }
            if (u == null || u.getHost() == null
                    || !("http".equals(u.getScheme()) || "https".equals(u.getScheme()))) {
                throw new IllegalArgumentException("url must look like https://host:8420, not \"" + baseUrl + "\"");
            }
            this.baseUrl = baseUrl.replaceAll("/+$", "");
        }

        /** API token, sent as Authorization: Bearer. */
        public Builder token(String token) {
            this.token = token;
            return this;
        }

        /** Route every call to this cluster member: /api/v1/hosts/&lt;host&gt;/… */
        public Builder host(String host) {
            this.host = host;
            return this;
        }

        /** An HttpClient of your own, e.g. with an SSLContext that trusts a self-signed host. */
        public Builder httpClient(HttpClient http) {
            this.http = http;
            return this;
        }

        public VirtoMirClient build() {
            return new VirtoMirClient(this);
        }
    }

    private static String seg(String s) {
        return URLEncoder.encode(s, StandardCharsets.UTF_8).replace("+", "%20");
    }

    private String path(String p) {
        if (host != null && !host.isEmpty() && p.startsWith("/api/v1/")) {
            return "/api/v1/hosts/" + seg(host) + p.substring("/api/v1".length());
        }
        return p;
    }

    /** Sends one request; body (if not null) is sent as JSON. Returns the answer, or null when it is empty. */
    public JsonNode request(String method, String p, Object body) {
        try {
            HttpRequest.Builder rb = HttpRequest.newBuilder(URI.create(baseUrl + path(p)))
                    .timeout(Duration.ofMinutes(2))
                    .header("Accept", "application/json");
            if (token != null && !token.isEmpty()) {
                rb.header("Authorization", "Bearer " + token);
            }
            if (body != null) {
                rb.header("Content-Type", "application/json");
                rb.method(method, HttpRequest.BodyPublishers.ofByteArray(JSON.writeValueAsBytes(body)));
            } else {
                rb.method(method, HttpRequest.BodyPublishers.noBody());
            }
            HttpResponse<String> res = http.send(rb.build(), HttpResponse.BodyHandlers.ofString());
            String text = res.body() == null ? "" : res.body();
            if (res.statusCode() >= 300) {
                String msg = text.trim();
                try {
                    JsonNode e = JSON.readTree(text);
                    if (e != null && e.hasNonNull("error")) {
                        msg = e.get("error").asText();
                    }
                } catch (IOException ignored) {
                    // not JSON
                }
                throw new VirtoMirException(res.statusCode(), msg);
            }
            return text.isBlank() ? null : JSON.readTree(text);
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IllegalStateException("interrupted", e);
        }
    }

    private <T> List<T> list(JsonNode n, String field, TypeReference<List<T>> type) {
        if (n == null || !n.hasNonNull(field)) {
            return List.of();
        }
        return JSON.convertValue(n.get(field), type);
    }

    // ---- machines --------------------------------------------------------------------

    public List<Machine> listMachines() {
        return list(request("GET", "/api/v1/domains", null), "domains", new TypeReference<>() { });
    }

    /** The machine page's full model of one machine. */
    public JsonNode getMachine(String uuid) {
        return request("GET", "/api/v1/domains/" + seg(uuid), null);
    }

    /** Finds a machine by name or UUID. */
    public Optional<Machine> findMachine(String nameOrUuid) {
        String want = nameOrUuid.toLowerCase(Locale.ROOT);
        return listMachines().stream()
                .filter(m -> nameOrUuid.equals(m.name()) || (m.uuid() != null && m.uuid().toLowerCase(Locale.ROOT).equals(want)))
                .findFirst();
    }

    public void power(String uuid, PowerAction action) {
        request("POST", "/api/v1/domains/" + seg(uuid) + "/actions/" + action.wire(), null);
    }

    public void setAutostart(String uuid, boolean enabled) {
        request("PUT", "/api/v1/domains/" + seg(uuid) + "/autostart", Map.of("enabled", enabled));
    }

    /** Deletes a machine; with removeStorage its disks go too. */
    public void deleteMachine(String uuid, boolean removeStorage) {
        request("DELETE", "/api/v1/domains/" + seg(uuid) + (removeStorage ? "?storage=remove" : ""), null);
    }

    // ---- snapshots -------------------------------------------------------------------

    public List<Snapshot> listSnapshots(String uuid) {
        return list(request("GET", "/api/v1/domains/" + seg(uuid) + "/snapshots", null), "snapshots", new TypeReference<>() { });
    }

    /** Takes a snapshot and returns its name (an empty name lets VirtoMir pick one). */
    public String createSnapshot(String uuid, String name, String description) {
        JsonNode r = request("POST", "/api/v1/domains/" + seg(uuid) + "/snapshots",
                Map.of("name", name == null ? "" : name, "description", description == null ? "" : description));
        return r == null ? "" : r.path("name").asText("");
    }

    /** Goes back to a snapshot. With safety the current state is snapshotted first. */
    public JsonNode revertSnapshot(String uuid, String name, boolean safety) {
        return request("POST", "/api/v1/domains/" + seg(uuid) + "/snapshots/" + seg(name) + "/revert"
                + (safety ? "" : "?safety=false"), null);
    }

    public void deleteSnapshot(String uuid, String name) {
        request("DELETE", "/api/v1/domains/" + seg(uuid) + "/snapshots/" + seg(name), null);
    }
}
