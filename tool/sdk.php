<?php

declare(strict_types=1);

// Parse supplied files as data. Composer autoload is trusted project code, not a sandbox.
require '/var/www/html/vendor/autoload.php';

$request = json_decode($argv[1], true, flags: JSON_THROW_ON_ERROR);
$root = '/var/www/html';
if (!is_array($request['files']) || $request['files'] === [] || count($request['files']) > 64) {
    throw new RuntimeException('Invalid SDK input');
}
$parser = (new PhpParser\ParserFactory)->createForNewestSupportedVersion();
$finder = new PhpParser\NodeFinder;
$files = [];
$facts = [];
foreach ($request['files'] as $relative) {
    $path = realpath($root.'/'.$relative);
    if ($path === false || !str_starts_with($path, $root.'/') || !is_file($path) || filesize($path) > 32 * 1024 * 1024) {
        throw new RuntimeException('Source is missing, too large or outside the SDK root');
    }
    $source = file_get_contents($path);
    $hash = hash('sha256', $source);
    $files[] = ['path' => $relative, 'sha256' => $hash, 'bytes' => strlen($source)];
    $nodes = $parser->parse($source);
    foreach ($finder->find($nodes ?? [], static fn (PhpParser\Node $node): bool =>
        $node instanceof PhpParser\Node\Stmt\ClassMethod
        || $node instanceof PhpParser\Node\Expr\MethodCall
        || $node instanceof PhpParser\Node\Expr\StaticCall
    ) as $node) {
        $start = $node->getStartFilePos();
        $stop = $node->getEndFilePos() + 1;
        if ($start < 0 || $stop <= $start || $stop > strlen($source)) {
            throw new RuntimeException('Invalid parser source range');
        }
        $facts[] = [
            'file' => $relative,
            'kind' => $node->getType(),
            'name' => $node->name instanceof PhpParser\Node\Identifier ? $node->name->toString() : '{dynamic}',
            'start' => $start,
            'stop' => $stop,
            'line' => $node->getStartLine(),
            'quote' => substr($source, $start, $stop - $start),
            'source_sha256' => $hash,
        ];
    }
}
echo json_encode([
    'version' => 'sdk/2',
    'evidence_kind' => 'syntax_only',
    'runtime' => [
        'php' => PHP_VERSION, 'os' => PHP_OS, 'arch' => php_uname('m'),
        'parser_version' => Composer\InstalledVersions::getPrettyVersion('nikic/php-parser'),
        'parser_reference' => Composer\InstalledVersions::getReference('nikic/php-parser') ?? '',
        'composer_lock_sha256' => hash_file('sha256', $root.'/composer.lock'),
        'sdk_sha256' => $request['sdk_sha256'],
    ],
    'files' => $files,
    'facts' => $facts,
], JSON_THROW_ON_ERROR | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES), "\n";
