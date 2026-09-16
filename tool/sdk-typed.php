<?php

declare(strict_types=1);

// Экспортёр типизированных фактов PHPStan/Larastan (docs/php-sdk-contract.md, формат sdk/3).
// Загружается через --autoload-file до компиляции DI-контейнера PHPStan, поэтому объявляет
// только классы и не имеет побочных эффектов при подключении. Исходники проекта читаются
// лишь для цитат и хэшей; экспортёр ничего не усекает — лимиты проверяет Go-ядро.

namespace SpecAudit;

use PhpParser\Node;
use PhpParser\Node\Expr;
use PhpParser\Node\Identifier;
use PhpParser\Node\Name;
use PHPStan\Analyser\Scope;
use PHPStan\Collectors\Collector;
use PHPStan\Node\CollectedDataNode;
use PHPStan\Node\FileNode;
use PHPStan\Node\InClassMethodNode;
use PHPStan\PhpDoc\StubFilesProvider;
use PHPStan\Reflection\ClassMemberReflection;
use PHPStan\Reflection\ClassReflection;
use PHPStan\Reflection\ExtendedMethodReflection;
use PHPStan\Rules\Rule;
use PHPStan\Rules\RuleErrorBuilder;
use PHPStan\TrinaryLogic;
use PHPStan\Type\TypeCombinator;
use PHPStan\Type\VerbosityLevel;

final class Paths
{
    public const MOUNT = '/var/www/html/';

    /** Относительный путь под mount либо null (phar, рабочая область, чужие пути). */
    public static function relative(string $path): ?string
    {
        if (str_starts_with($path, 'phar://')) {
            return null;
        }
        if (str_contains($path, '/../') || str_contains($path, '/./')) {
            $real = realpath($path);
            if ($real !== false) {
                $path = $real;
            }
        }
        if (str_starts_with($path, self::MOUNT)) {
            $rel = substr($path, strlen(self::MOUNT));
            return $rel === '' ? null : $rel;
        }
        return null;
    }
}

/**
 * @implements Collector<Node, array<string, mixed>>
 */
final class FactCollector implements Collector
{
    private const MAX_QUOTE_LINES = 8;

    /** @var array<string, list<string>|null> */
    private array $lines = [];

    public function getNodeType(): string
    {
        return Node::class;
    }

    /** @return array<string, mixed>|null */
    public function processNode(Node $node, Scope $scope): ?array
    {
        if ($node instanceof FileNode) {
            $rel = Paths::relative($scope->getFile());
            return $rel === null ? null : ['type' => 'file', 'path' => $rel];
        }
        if ($node instanceof InClassMethodNode) {
            return $this->declaration($node, $scope);
        }
        if ($node instanceof Expr\MethodCall || $node instanceof Expr\StaticCall || $node instanceof Expr\NullsafeMethodCall) {
            return $this->call($node, $scope);
        }
        return null;
    }

    /** @return array<string, mixed>|null */
    private function declaration(InClassMethodNode $node, Scope $scope): ?array
    {
        $method = $node->getMethodReflection();
        $original = $node->getOriginalNode();
        $path = $this->citationPath($scope);
        if ($path === null) {
            return null;
        }
        $line = $original->name->getStartLine();
        $quote = $this->quote($scope, $line, $line);
        if ($quote === null) {
            return null;
        }
        $targets = [];
        $prototype = $method->getPrototype();
        if ($prototype->getDeclaringClass()->getName() !== $method->getDeclaringClass()->getName()) {
            $targets[] = $this->target($prototype->getDeclaringClass(), $prototype, $method->getName());
        }
        return [
            'type' => 'fact',
            'citation' => ['path' => $path, 'line_start' => $line, 'line_end' => $line, 'quote' => $quote],
            'syntax' => 'Stmt_ClassMethod',
            'name' => $method->getName(),
            'origin' => $this->origin($method),
            'resolution' => 'declared',
            'receiver_type' => '',
            'targets' => $targets,
        ];
    }

    /**
     * @param Expr\MethodCall|Expr\StaticCall|Expr\NullsafeMethodCall $node
     * @return array<string, mixed>|null
     */
    private function call(Expr $node, Scope $scope): ?array
    {
        $path = $this->citationPath($scope);
        if ($path === null) {
            return null;
        }
        $syntax = $node instanceof Expr\MethodCall ? 'Expr_MethodCall'
            : ($node instanceof Expr\StaticCall ? 'Expr_StaticCall' : 'Expr_NullsafeMethodCall');
        $start = $node->getStartLine();
        $end = $node->getEndLine();
        $nameNode = $node->name;
        $nameLine = $nameNode instanceof Node ? $nameNode->getStartLine() : $start;
        if ($end - $start + 1 > self::MAX_QUOTE_LINES) {
            $start = $end = $nameLine;
        }
        $quote = $this->quote($scope, $start, $end);
        if ($quote === null) {
            return null;
        }
        $base = [
            'type' => 'fact',
            'citation' => ['path' => $path, 'line_start' => $start, 'line_end' => $end, 'quote' => $quote],
            'syntax' => $syntax,
        ];
        if (!$nameNode instanceof Identifier) {
            return $base + ['name' => '{dynamic}', 'origin' => 'phpstan', 'resolution' => 'dynamic', 'receiver_type' => '', 'targets' => []];
        }
        $name = $nameNode->toString();
        if ($node instanceof Expr\StaticCall) {
            $receiver = $node->class instanceof Name
                ? $scope->resolveTypeByName($node->class)
                : $scope->getType($node->class)->getClassStringObjectType();
        } else {
            $receiver = $scope->getType($node->var);
            if ($node instanceof Expr\NullsafeMethodCall) {
                $receiver = TypeCombinator::removeNull($receiver);
            }
        }
        $targets = [];
        $origin = 'phpstan';
        foreach ($receiver->getObjectClassReflections() as $class) {
            if (!$class->hasMethod($name)) {
                continue;
            }
            $method = $class->getMethod($name, $scope);
            $declaring = $method->getDeclaringClass();
            $key = $declaring->getName() . '::' . $method->getName();
            if (isset($targets[$key])) {
                continue;
            }
            $targets[$key] = $this->target($declaring, $method, $method->getName());
            if ($this->origin($method) === 'larastan') {
                $origin = 'larastan';
            }
        }
        $targets = array_values($targets);
        $count = count($targets);
        if ($count === 0) {
            $resolution = 'unresolved';
        } elseif ($this->allVirtual($targets)) {
            $resolution = 'virtual';
        } elseif ($count === 1) {
            $resolution = 'resolved';
        } else {
            $resolution = 'ambiguous';
        }
        return $base + [
            'name' => $name,
            'origin' => $origin,
            'resolution' => $resolution,
            'receiver_type' => $receiver->describe(VerbosityLevel::typeOnly()),
            'targets' => $targets,
        ];
    }

    /** @return array<string, mixed> */
    private function target(ClassReflection $declaring, ClassMemberReflection $method, string $name): array
    {
        $file = '';
        $line = 0;
        $native = false;
        if ($declaring->hasNativeMethod($name)) {
            if ($declaring->isBuiltin()) {
                $native = true;
            } else {
                $reflection = $declaring->getNativeReflection()->getMethod($name);
                $fileName = $reflection->getFileName();
                $startLine = $reflection->getStartLine();
                if (is_string($fileName) && $fileName !== '' && is_int($startLine) && $startLine > 0) {
                    $rel = Paths::relative($fileName);
                    $file = $rel ?? $fileName;
                    $line = $startLine;
                } else {
                    $native = true;
                }
            }
        }
        $abstract = method_exists($method, 'isAbstract') ? $method->isAbstract() : false;
        $isAbstract = $abstract instanceof TrinaryLogic ? $abstract->yes() : (bool) $abstract;
        return [
            'class' => $declaring->getName(),
            'method' => $name,
            'file' => $file,
            'line' => $line,
            'interface' => $declaring->isInterface() || $isAbstract,
            'native' => $native,
        ];
    }

    /** @param list<array<string, mixed>> $targets */
    private function allVirtual(array $targets): bool
    {
        foreach ($targets as $target) {
            if ($target['file'] !== '' || $target['native']) {
                return false;
            }
        }
        return true;
    }

    private function origin(ExtendedMethodReflection $method): string
    {
        if (str_starts_with(get_class($method), 'Larastan\\')) {
            return 'larastan';
        }
        $prototype = $method->getPrototype();
        return str_starts_with(get_class($prototype), 'Larastan\\') ? 'larastan' : 'phpstan';
    }

    private function citationPath(Scope $scope): ?string
    {
        $file = $scope->isInTrait() ? $scope->getTraitReflection()->getFileName() : $scope->getFile();
        return is_string($file) ? Paths::relative($file) : null;
    }

    private function quote(Scope $scope, int $start, int $end): ?string
    {
        $file = $scope->isInTrait() ? $scope->getTraitReflection()->getFileName() : $scope->getFile();
        if (!is_string($file)) {
            return null;
        }
        if (!array_key_exists($file, $this->lines)) {
            $source = @file_get_contents($file);
            if ($source === false || !mb_check_encoding($source, 'UTF-8')) {
                $this->lines[$file] = null;
            } else {
                $trimmed = str_ends_with($source, "\n") ? substr($source, 0, -1) : $source;
                $this->lines[$file] = explode("\n", $trimmed);
            }
        }
        $lines = $this->lines[$file];
        if ($lines === null || $start < 1 || $end < $start || $end > count($lines)) {
            return null;
        }
        $quote = implode("\n", array_slice($lines, $start - 1, $end - $start + 1));
        return str_ends_with($quote, "\r") ? substr($quote, 0, -1) : $quote;
    }
}

/**
 * @implements Rule<CollectedDataNode>
 */
final class FactSink implements Rule
{
    /**
     * @param list<string> $allConfigFiles
     * @param list<string> $bootstrapFiles
     * @param list<string> $scanFiles
     * @param list<string> $scanDirectories
     * @param list<string> $migrationPaths
     * @param list<string> $schemaPaths
     */
    public function __construct(
        private StubFilesProvider $stubFilesProvider,
        private array $allConfigFiles,
        private array $bootstrapFiles,
        private array $scanFiles,
        private array $scanDirectories,
        private array $migrationPaths,
        private array $schemaPaths,
        private string $profile,
        private string $sdkSha256,
    ) {
    }

    public function getNodeType(): string
    {
        return CollectedDataNode::class;
    }

    public function processNode(Node $node, Scope $scope): array
    {
        $files = [];
        $facts = [];
        foreach ($node->get(FactCollector::class) as $items) {
            foreach ($items as $item) {
                if ($item['type'] === 'file') {
                    $files[$item['path']] = true;
                } else {
                    unset($item['type']);
                    $facts[] = $item;
                }
            }
        }
        $basisPaths = [];
        foreach ([$this->allConfigFiles, $this->bootstrapFiles, $this->scanFiles, $this->stubFilesProvider->getStubFiles()] as $list) {
            foreach ($list as $path) {
                $this->addBasis($basisPaths, $path);
            }
        }
        foreach ($this->scanDirectories as $dir) {
            $this->addTree($basisPaths, $dir, ['php']);
        }
        if ($this->profile === 'laravel') {
            $migrations = $this->migrationPaths === [] ? [Paths::MOUNT . 'database/migrations'] : $this->migrationPaths;
            foreach ($migrations as $dir) {
                $this->addTree($basisPaths, $this->absolute($dir), ['php']);
            }
            $schemas = $this->schemaPaths === [] ? [Paths::MOUNT . 'database/schema'] : $this->schemaPaths;
            foreach ($schemas as $dir) {
                $this->addTree($basisPaths, $this->absolute($dir), ['sql', 'dump']);
            }
        }
        foreach (get_included_files() as $included) {
            $rel = Paths::relative($included);
            if ($rel !== null && !str_starts_with($rel, 'vendor/')) {
                $basisPaths[$rel] = true;
            }
        }
        $bootstrap = [];
        foreach ($this->bootstrapFiles as $path) {
            $rel = Paths::relative($path);
            if ($rel !== null) {
                $bootstrap[$rel] = true;
            }
        }
        $envelope = [
            'version' => 'sdk/3',
            'evidence_kind' => 'typed',
            'profile' => $this->profile,
            'runtime' => [
                'php' => PHP_VERSION,
                'os' => PHP_OS,
                'arch' => php_uname('m'),
                'composer_lock_sha256' => hash_file('sha256', Paths::MOUNT . 'composer.lock'),
                'sdk_sha256' => $this->sdkSha256,
            ],
            'files' => $this->describe(array_keys($files)),
            'bootstrap_files' => $this->sorted(array_keys($bootstrap)),
            'basis' => $this->describe(array_keys($basisPaths)),
            'facts' => $this->sortFacts($this->dedupe($facts)),
        ];
        $json = json_encode($envelope, JSON_THROW_ON_ERROR | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
        return [RuleErrorBuilder::message($json)->identifier('specAudit.envelope')->nonIgnorable()->build()];
    }

    /** @param array<string, true> $basis */
    private function addBasis(array &$basis, string $path): void
    {
        $rel = Paths::relative($path);
        if ($rel !== null && is_file(Paths::MOUNT . $rel)) {
            $basis[$rel] = true;
        }
    }

    /**
     * @param array<string, true> $basis
     * @param list<string> $extensions
     */
    private function addTree(array &$basis, string $dir, array $extensions): void
    {
        if (!is_dir($dir)) {
            return;
        }
        $iterator = new \RecursiveIteratorIterator(new \RecursiveDirectoryIterator($dir, \FilesystemIterator::SKIP_DOTS));
        foreach ($iterator as $file) {
            if ($file->isFile() && in_array(strtolower($file->getExtension()), $extensions, true)) {
                $this->addBasis($basis, $file->getPathname());
            }
        }
    }

    private function absolute(string $path): string
    {
        return str_starts_with($path, '/') ? $path : Paths::MOUNT . $path;
    }

    /**
     * @param list<string> $paths
     * @return list<array{path: string, sha256: string, bytes: int}>
     */
    private function describe(array $paths): array
    {
        $out = [];
        foreach ($this->sorted($paths) as $rel) {
            $abs = Paths::MOUNT . $rel;
            $out[] = ['path' => $rel, 'sha256' => hash_file('sha256', $abs), 'bytes' => filesize($abs)];
        }
        return $out;
    }

    /**
     * @param list<string> $values
     * @return list<string>
     */
    private function sorted(array $values): array
    {
        sort($values, SORT_STRING);
        return $values;
    }

    /**
     * @param list<array<string, mixed>> $facts
     * @return list<array<string, mixed>>
     */
    private function dedupe(array $facts): array
    {
        $seen = [];
        $out = [];
        foreach ($facts as $fact) {
            $key = json_encode([$fact['citation'], $fact['syntax'], $fact['name'], $fact['targets']], JSON_THROW_ON_ERROR);
            if (isset($seen[$key])) {
                continue;
            }
            $seen[$key] = true;
            $out[] = $fact;
        }
        return $out;
    }

    /**
     * @param list<array<string, mixed>> $facts
     * @return list<array<string, mixed>>
     */
    private function sortFacts(array $facts): array
    {
        usort($facts, static function (array $a, array $b): int {
            $ca = $a['citation'];
            $cb = $b['citation'];
            return [$ca['path'], $ca['line_start'], $ca['line_end'], $a['syntax'], $a['name'], $a['targets'][0]['class'] ?? '']
                <=> [$cb['path'], $cb['line_start'], $cb['line_end'], $b['syntax'], $b['name'], $b['targets'][0]['class'] ?? ''];
        });
        return $facts;
    }
}
