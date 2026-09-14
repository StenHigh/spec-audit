<?php

declare(strict_types=1);

namespace TracePilot;

use PhpParser\Node;
use PhpParser\Node\Expr;
use PhpParser\Node\Expr\MethodCall;
use PhpParser\Node\Expr\NullsafeMethodCall;
use PhpParser\Node\Expr\StaticCall;
use PhpParser\Node\Identifier;
use PhpParser\Node\Name;
use PHPStan\Analyser\Scope;
use PHPStan\Rules\Rule;
use PHPStan\Rules\RuleErrorBuilder;
use PHPStan\Type\ObjectType;
use PHPStan\Type\VerbosityLevel;

/** @implements Rule<Expr> */
final class PublicCallProbeRule implements Rule
{
    public function getNodeType(): string
    {
        return Expr::class;
    }

    public function processNode(Node $node, Scope $scope): array
    {
        if (! ($node instanceof MethodCall || $node instanceof NullsafeMethodCall || $node instanceof StaticCall)) {
            return [];
        }
        if (! $node->name instanceof Identifier) {
            return [];
        }
        $name = $node->name->toString();
        $receiver = $node instanceof StaticCall
            ? ($node->class instanceof Name ? new ObjectType($scope->resolveName($node->class)) : $scope->getType($node->class))
            : $scope->getType($node->var);
        $hasMethod = $receiver->hasMethod($name);
        $method = $hasMethod->yes() ? $receiver->getMethod($name, $scope) : null;
        $declaring = $method?->getDeclaringClass();
        $frameworkOrigin = null;
        $fact = [
            'file' => $scope->getFile(),
            'line' => $node->getStartLine(),
            'caller_class' => $scope->getClassReflection()?->getName(),
            'caller_method' => $scope->getFunction()?->getName(),
            'syntax' => $node->getType(),
            'written_method' => $name,
            'receiver_type' => $receiver->describe(VerbosityLevel::precise()),
            'has_method' => $hasMethod->describe(),
            'result_type' => $scope->getType($node)->describe(VerbosityLevel::precise()),
            'target_class' => $declaring?->getName(),
            'target_method' => $method?->getName(),
            'target_file' => $declaring?->getFileName(),
            'target_is_interface' => $declaring?->isInterface(),
            'target_is_native' => $declaring !== null && $declaring->hasNativeMethod($method->getName()),
            'reflection_class' => $method === null ? null : get_class($method),
            'framework_origin' => $frameworkOrigin,
        ];

        return [RuleErrorBuilder::message(json_encode($fact, JSON_THROW_ON_ERROR | JSON_UNESCAPED_SLASHES))
            ->identifier('tracePilot.fact')->nonIgnorable()->build()];
    }
}
