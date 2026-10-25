package dev.pamon;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.net.URI;
import java.net.URLEncoder;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Asks pamon whether an API it monitors should be called now. Reads
 * {base}/ai/targets/{id}.json, whose retryAdvice.action is retry, backoff, wait (pamon
 * sees it failing), skip (announced maintenance) or unknown. pamon is a signal, not a
 * dependency: when it cannot be read, or its observation is past freshUntil, the advice
 * is "unknown" and calls go ahead.
 */
public final class PamonGuard {
    public static final String DEFAULT_BASE = "https://pamon.hyunsuhahaha.workers.dev";

    /** pamon's advice for one target. */
    public record Advice(String action, String reason) {
        public boolean blocks() { return "wait".equals(action) || "skip".equals(action); }
        static Advice unknown(String why) { return new Advice("unknown", why); }
    }

    private record Entry(Instant readAt, JsonNode target) {}

    private final String base;
    private final Duration minRefresh;
    private final HttpClient http;
    private final Clock clock;
    private final ObjectMapper json = new ObjectMapper();
    private final Map<String, Entry> cache = new ConcurrentHashMap<>();
    private final Map<String, Object> locks = new ConcurrentHashMap<>();

    public PamonGuard() { this(DEFAULT_BASE, Duration.ofMinutes(1), Clock.systemUTC()); }

    public PamonGuard(String base, Duration minRefresh, Clock clock) {
        this.base = base.replaceAll("/$", "");
        this.minRefresh = minRefresh;
        this.clock = clock;
        this.http = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(3)).build();
    }

    /** The current advice; cached for minRefresh, one read at a time per target. */
    public Advice advice(String id) {
        Entry e;
        synchronized (locks.computeIfAbsent(id, k -> new Object())) {
            e = cache.get(id);
            Instant now = clock.instant();
            if (e == null || !now.isBefore(e.readAt().plus(minRefresh))) {
                JsonNode t = read(id);
                e = new Entry(now, t != null ? t : (e != null ? e.target() : null));
                cache.put(id, e);
            }
        }
        JsonNode t = e.target();
        if (t == null || !t.hasNonNull("freshUntil")) return Advice.unknown("no observation");
        if (!clock.instant().isBefore(Instant.parse(t.get("freshUntil").asText()))) return Advice.unknown("observation expired");
        JsonNode a = t.path("retryAdvice");
        return new Advice(a.path("action").asText("unknown"), a.path("reason").asText(""));
    }

    private JsonNode read(String id) {
        try {
            HttpRequest req = HttpRequest.newBuilder(URI.create(base + "/ai/targets/" + URLEncoder.encode(id, StandardCharsets.UTF_8) + ".json"))
                    .timeout(Duration.ofSeconds(5)).header("User-Agent", "pamon-resilience4j-example").GET().build();
            HttpResponse<String> resp = http.send(req, HttpResponse.BodyHandlers.ofString());
            if (resp.statusCode() != 200) return null;
            for (JsonNode t : json.readTree(resp.body()).path("targets")) {
                if (id.equals(t.path("id").asText())) return t;
            }
            return null;
        } catch (Exception e) {
            if (e instanceof InterruptedException) Thread.currentThread().interrupt();
            return null; // fail open
        }
    }
}
