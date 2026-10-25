package dev.pamon;

import static org.junit.jupiter.api.Assertions.*;

import com.sun.net.httpserver.HttpServer;
import io.github.resilience4j.circuitbreaker.CallNotPermittedException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class PamonAwareTest {
    HttpServer pamon;
    final AtomicReference<String> action = new AtomicReference<>("retry");
    final AtomicInteger reads = new AtomicInteger();
    String base;

    @BeforeEach
    void start() throws Exception {
        pamon = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        pamon.createContext("/ai/targets/", ex -> {
            reads.incrementAndGet();
            String body = "{\"targets\":[{\"id\":\"x\",\"freshUntil\":\"" + Instant.now().plusSeconds(600)
                    + "\",\"retryAdvice\":{\"action\":\"" + action.get() + "\",\"reason\":\"test\"}}]}";
            byte[] b = body.getBytes(StandardCharsets.UTF_8);
            ex.sendResponseHeaders(200, b.length);
            ex.getResponseBody().write(b);
            ex.close();
        });
        pamon.start();
        base = "http://127.0.0.1:" + pamon.getAddress().getPort();
    }

    @AfterEach
    void stop() { pamon.stop(0); }

    PamonAware aware(Duration refresh) {
        return PamonAware.defaults(new PamonGuard(base, refresh, Clock.systemUTC()), "x");
    }

    @Test
    void pamonSeesTheOutageSoNothingIsCalled() {
        action.set("wait");
        PamonAware api = aware(Duration.ofMinutes(1));
        AtomicInteger calls = new AtomicInteger();
        for (int i = 0; i < 50; i++) {
            assertThrows(PamonAware.PamonSaysNo.class, () -> api.call(calls::incrementAndGet));
        }
        assertEquals(0, calls.get());
        assertEquals(1, reads.get(), "one read for 50 calls");
    }

    @Test
    void withoutPamonTheBreakerStillOpens() {
        PamonAware api = PamonAware.defaults(new PamonGuard("http://127.0.0.1:1", Duration.ofMinutes(1), Clock.systemUTC()), "x");
        AtomicInteger calls = new AtomicInteger();
        for (int i = 0; i < 50; i++) {
            assertThrows(RuntimeException.class, () -> api.call(() -> {
                calls.incrementAndGet();
                throw new IllegalStateException("503");
            }));
        }
        assertTrue(calls.get() <= 6, "calls: " + calls.get());
        assertThrows(CallNotPermittedException.class, () -> api.call(() -> "x"));
    }

    @Test
    void retriesStopWhenPamonConfirms() {
        PamonAware api = aware(Duration.ZERO);
        AtomicInteger calls = new AtomicInteger();
        assertThrows(PamonAware.PamonSaysNo.class, () -> api.call(() -> {
            calls.incrementAndGet();
            action.set("wait"); // pamon's next check sees it
            throw new IllegalStateException("503");
        }));
        assertEquals(1, calls.get());
    }

    @Test
    void aBlipIsRetriedAndA4xxIsNot() {
        PamonAware api = aware(Duration.ofMinutes(1));
        AtomicInteger calls = new AtomicInteger();
        assertEquals("ok", api.call(() -> {
            if (calls.incrementAndGet() < 2) throw new IllegalStateException("blip");
            return "ok";
        }));
        calls.set(0);
        assertThrows(PamonAware.Permanent.class, () -> api.call(() -> {
            calls.incrementAndGet();
            throw new PamonAware.Permanent("404");
        }));
        assertEquals(1, calls.get());
    }

    @Test
    void maintenanceIsSkipped() {
        action.set("skip");
        PamonAware.PamonSaysNo e = assertThrows(PamonAware.PamonSaysNo.class, () -> aware(Duration.ofMinutes(1)).call(() -> 1));
        assertEquals("skip", e.advice.action());
    }
}
