package dev.pamon;

import io.github.resilience4j.circuitbreaker.CallNotPermittedException;
import io.github.resilience4j.circuitbreaker.CircuitBreaker;
import io.github.resilience4j.circuitbreaker.CircuitBreakerConfig;
import io.github.resilience4j.core.IntervalFunction;
import io.github.resilience4j.retry.Retry;
import io.github.resilience4j.retry.RetryConfig;
import java.time.Duration;
import java.util.function.Supplier;

/**
 * Resilience4j Retry + CircuitBreaker around a call, with pamon asked before every attempt:
 * when pamon sees the API failing (or a maintenance window is announced) the attempt is not
 * made and no retry follows. That stops the retry storm that gets an IP banned while the
 * provider is down.
 *
 * <pre>
 * PamonAware api = PamonAware.defaults(new PamonGuard(), "github-api");
 * String zen = api.call(() -> httpGet("https://api.github.com/zen"));
 * </pre>
 */
public final class PamonAware {
    /** Thrown instead of calling when pamon advises against it. */
    public static final class PamonSaysNo extends RuntimeException {
        public final PamonGuard.Advice advice;
        PamonSaysNo(PamonGuard.Advice a) {
            super("pamon: " + a.action() + (a.reason().isEmpty() ? "" : " (" + a.reason() + ")"));
            this.advice = a;
        }
    }

    /** Marks a failure a retry cannot fix (a 4xx answer). */
    public static final class Permanent extends RuntimeException {
        public Permanent(String message) { super(message); }
    }

    private final PamonGuard guard;
    private final String target;
    private final CircuitBreaker breaker;
    private final Retry retry;

    public PamonAware(PamonGuard guard, String target, CircuitBreaker breaker, Retry retry) {
        this.guard = guard;
        this.target = target;
        this.breaker = breaker;
        this.retry = retry;
    }

    /** 3 attempts with jittered exponential backoff; the breaker opens at 50% of the last 10 calls failing. */
    public static PamonAware defaults(PamonGuard guard, String target) {
        RetryConfig rc = RetryConfig.custom()
                .maxAttempts(3)
                .intervalFunction(IntervalFunction.ofExponentialRandomBackoff(Duration.ofMillis(200), 2.0, 0.5, Duration.ofSeconds(5)))
                // pamon said no, the breaker is open, or a 4xx: retrying will not help
                .ignoreExceptions(PamonSaysNo.class, CallNotPermittedException.class, Permanent.class)
                .build();
        CircuitBreakerConfig cc = CircuitBreakerConfig.custom()
                .slidingWindowSize(10).minimumNumberOfCalls(5).failureRateThreshold(50)
                .waitDurationInOpenState(Duration.ofSeconds(30))
                // pamon's "no" is not a failure of the API call, and a 4xx is an answer
                .ignoreExceptions(PamonSaysNo.class, Permanent.class)
                .build();
        return new PamonAware(guard, target, CircuitBreaker.of(target, cc), Retry.of(target, rc));
    }

    public <T> T call(Supplier<T> call) {
        Supplier<T> asked = () -> {
            PamonGuard.Advice a = guard.advice(target);
            if (a.blocks()) throw new PamonSaysNo(a);
            return call.get();
        };
        return Retry.decorateSupplier(retry, CircuitBreaker.decorateSupplier(breaker, asked)).get();
    }

    public CircuitBreaker breaker() { return breaker; }
}
